//go:build e2e

// helpers_test.go W 用例共用 helper：客户端引导（loginOK）/ 主机供给
// （provisionExecHost）/ 帧解码与 WS 打开 / 审计链断言与轮询。
package webapi

import (
	"net/http"
	"testing"
	"time"

	"gengar-pro/internal/audit"
	"gengar-pro/internal/e2e"
)

// loginOK 新建客户端并登录、断言 200（引导 admin 的常规路径）。错误
// 密码 401 / 限速 429 是 W01 的断言对象——那条路径走裸 login 拿状态码。
func loginOK(t *testing.T, p *serverProc, user, pass string) *apiClient {
	t.Helper()
	c := newAPIClient(p.baseURL)
	if code := c.login(t, user, pass); code != http.StatusOK {
		t.Fatalf("login %s = %d; want 200", user, code)
	}
	return c
}

// listHostNames 取清单内主机名集合。W02 预置主机与 REST 所建并存，
// 断言只锚特定主机的存在/消失，不断言清单长度。
func listHostNames(t *testing.T, c *apiClient) map[string]bool {
	t.Helper()
	m := c.json(t, http.MethodGet, "/api/hosts", nil)
	names := map[string]bool{}
	arr, _ := m["hosts"].([]any)
	for _, h := range arr {
		hm, ok := h.(map[string]any)
		if !ok {
			continue
		}
		if n, _ := hm["name"].(string); n != "" {
			names[n] = true
		}
	}
	return names
}

// sshHostPass 容器 sshd 的 root 密码（同 internal/e2e 的 e2ePass；integration
// 的 sshd.Dockerfile 内置）。用真实凭据当脱敏标记最硬：它必须真的能拨通
// 容器，又绝不允许出现在审计里。
const sshHostPass = "gengar-it-pass"

// provisionExecHost 经 REST 建一台可真执行的目标机，返回主机 id：
// Docker sshd 容器由 StartHosts 起好，主机条目走 REST 建——SeedHosts 播
// 的 e2e-pass 密码引用只在 headless 的 --secrets 注入下可解析，server 用
// 自己的加密秘密库且无注入口，execute_command 要真拨通必须经 REST 把
// 密码写进库（W02 已验证的路径）。name 是清单名：W03/W04 用容器名
// e2e-host-N（桩工具调用的 hosts 与审计的 Hosts 断言都引用它）；W05 用
// iso-host-a/b（无工具流，名只作隔离断言的锚点）。id 供 W05 拿别家主机
// 做 404/scope 探测（W03/W04 不需要，忽略返回值即可）。
func provisionExecHost(t *testing.T, c *apiClient, name string, target e2e.HostSpec) string {
	t.Helper()
	m := c.json(t, http.MethodPost, "/api/hosts", map[string]any{
		"name": name, "addr": target.Addr, "port": target.Port,
		"user": "root", "authMethod": "password", "password": sshHostPass,
	})
	id, _ := m["id"].(string)
	if id == "" {
		t.Fatalf("建执行目标主机 %s 返回空 id: %v", name, m)
	}
	return id
}

// jsonStrings 把 JSON any 数组读成字符串集合（confirm 帧 reqs 的 Hosts
// 等嵌套字段的包含断言用；nil/形态不符返回空集）。
func jsonStrings(v any) map[string]bool {
	m := map[string]bool{}
	arr, _ := v.([]any)
	for _, it := range arr {
		if s, ok := it.(string); ok {
			m[s] = true
		}
	}
	return m
}

// frameInt 读帧的数值字段（seq/next 等在 map 里是 float64）。
func frameInt(f map[string]any, key string) int {
	n, _ := f[key].(float64)
	return int(n)
}

// openChatWS 拨对话 WS（session 空 = 服务端新建）并消费首帧 hello，
// 返回客户端与会话 id（hello 必为首帧是服务端语义，waitEvent 的跳读
// 会把它当普通帧丢掉，所以这里单独 next 一次）。
func openChatWS(t *testing.T, c *apiClient) (*wsClient, string) {
	t.Helper()
	w := dialChatWS(t, c, "", 0)
	hello := w.next(t, 10*time.Second)
	if hello["t"] != "hello" {
		t.Fatalf("WS 首帧 = %v; want hello", hello["t"])
	}
	sid, _ := hello["session"].(string)
	if sid == "" {
		t.Fatalf("hello 未带 session: %v", hello)
	}
	return w, sid
}

// auditStep 是审计链断言的一环：tool 名 + 谓词（nil = 仅按 tool 匹配）。
type auditStep struct {
	tool string
	ok   func(e audit.Entry) bool
}

// assertAuditChain 验证 steps 按给定顺序在 entries 中出现：单趟扫描，
// 当前步的 (tool, 谓词) 命中即前进。无关条目（login_ok/host_save 等
// 背景）穿插不破坏链——断的是因果序而非紧邻。全部步命中才过；否则
// 报错附停在第几步与全量 tool 序列辅助诊断。
func assertAuditChain(t *testing.T, entries []audit.Entry, steps []auditStep) {
	t.Helper()
	hit := 0
	for _, e := range entries {
		if hit < len(steps) && e.Tool == steps[hit].tool && (steps[hit].ok == nil || steps[hit].ok(e)) {
			hit++
		}
	}
	if hit < len(steps) {
		var tools []string
		for _, e := range entries {
			tools = append(tools, e.Tool)
		}
		t.Fatalf("审计链断在第 %d/%d 步（%s），已见 tool 序列: %v", hit+1, len(steps), steps[hit].tool, tools)
	}
}

// waitAuditEntries 轮询 ReadAudit 直至谓词命中并返回当时的条目快照。
// 服务端落审计在自己的 goroutine 里（断线取消链尤其异步：读循环感知
// 断链 → unsubscribe → onIdle → CancelPendings → 落盘），用例侧收到
// 「已发生」的信号（WS 帧/REST 响应）不保证条目已 flush——轮询是唯一
// 确定性等法；上界 timeout，超时附 tool 序列诊断。
func waitAuditEntries(t *testing.T, dataDir string, timeout time.Duration, pred func([]audit.Entry) bool) []audit.Entry {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		es := e2e.ReadAudit(t, dataDir)
		if pred(es) {
			return es
		}
		if time.Now().After(deadline) {
			var tools []string
			for _, e := range es {
				tools = append(tools, e.Tool)
			}
			t.Fatalf("等待审计条目超时（%v），已见 tool 序列: %v", timeout, tools)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
