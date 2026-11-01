//go:build e2e

package webapi

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"gengar-pro/internal/audit"
	"gengar-pro/internal/e2e"
)

// TestMain：server 二进制的临时目录是进程级缓存（sync.Once 编译一次、
// 全部用例复用），生命周期长于任何单个用例，不能挂 t.Cleanup——进程
// 退出时统一清理（同 internal/e2e 的 headless 档）。
func TestMain(m *testing.M) {
	code := m.Run()
	if serverBinDir != "" {
		_ = os.RemoveAll(serverBinDir)
	}
	os.Exit(code)
}

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

// TestServerSmoke 冒烟：真实 gengar-server 子进程上走通 登录 → me
// （首用户 admin 的角色提权由 ensureAdmin 兑现）→ 登出 → 401。桩 LLM
// 用空脚本兜底（冒烟不触发对话，仅为 startServer 的 env 形位）。
func TestServerSmoke(t *testing.T) {
	dataDir := t.TempDir()
	stub := e2e.StartStubLLM(t, nil)
	p := startServer(t, dataDir, stub.URL(), "e2e-stub-key")

	c := loginOK(t, p, adminUser, adminPass)
	me := c.json(t, http.MethodGet, "/api/me", nil)
	if me["username"] != adminUser || me["role"] != "admin" {
		t.Fatalf("/api/me = %v; want username=admin role=admin", me)
	}
	c.json(t, http.MethodPost, "/api/logout", nil)
	// 登出后 cookie 失效：/api/me 走 do 拿状态码（401 是预期，json 会 Fatal）。
	resp, _ := c.do(t, http.MethodGet, "/api/me", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("logout 后 /api/me = %d; want 401", resp.StatusCode)
	}
}

// TestW01Login 登录流全链路：正确密码 200 + me、错误密码 401、连续失败的
// 指数限速 429、登出后 cookie 失效、审计三事件（login_ok / login_fail /
// logout）、冷却到期后成功登录恢复 200。
//
// 限速语义（webapi/auth.go loginLimit.fail）：每记一次失败（计数 +1 后）
// 置冷却 min(1s<<失败数, 30s)——首败即 2s、其后 4s/8s… 翻倍；冷却内的
// 尝试直接 429——不跑 bcrypt、不延长冷却、不清计数；成功登录清零。
// 断言锚语义不锚时序：429「出现即可」（首败后冷却 2s，本机往返远小于
// 它，但第几次触发随 bcrypt 耗时浮动，循环兜住）；
// login_fail 凑下限 2 的第二次 401 用「轮询到冷却过期」保证确定性——
// 429 不延长冷却，等待上界即冷却上限 30s。
func TestW01Login(t *testing.T) {
	dataDir := t.TempDir()
	stub := e2e.StartStubLLM(t, nil) // 空脚本兜底：W01 不触发 LLM
	p := startServer(t, dataDir, stub.URL(), "e2e-stub-key")

	// 正确密码 → 200 + me（admin 角色）
	c := loginOK(t, p, adminUser, adminPass)
	me := c.json(t, http.MethodGet, "/api/me", nil)
	if me["username"] != adminUser || me["role"] != "admin" {
		t.Fatalf("/api/me = %v; want username=admin role=admin", me)
	}

	// 错误密码 → 401；紧接着的尝试落在冷却内 → 429。若往返偶超 2s（冷却
	// 已过）会再多一次 401——冷却随失败数翻倍，只会更早撞上 429。
	const wrongPass = "definitely-wrong-pass"
	saw429 := false
	for i := 0; !saw429 && i < 5; i++ {
		switch code := c.login(t, adminUser, wrongPass); code {
		case http.StatusTooManyRequests:
			saw429 = true
		case http.StatusUnauthorized:
		default:
			t.Fatalf("错误密码登录 = %d; want 401/429", code)
		}
	}
	if !saw429 {
		t.Fatal("连续 5 次错误密码仍未触发 429 限速")
	}

	// 冷却过期后的下一次失败必 401——凑足 login_fail >= 2 的确定性下限。
	// 轮询 500ms 一发；429 期间不延长冷却，最坏等到上限 30s 过去。
	coolDeadline := time.Now().Add(45 * time.Second)
	secondFail := false
	for !secondFail {
		if time.Now().After(coolDeadline) {
			t.Fatal("45s 内未出现第二次 401（限速冷却未按预期过期）")
		}
		time.Sleep(500 * time.Millisecond)
		switch code := c.login(t, adminUser, wrongPass); code {
		case http.StatusUnauthorized:
			secondFail = true
		case http.StatusTooManyRequests:
		default:
			t.Fatalf("错误密码登录 = %d; want 401/429", code)
		}
	}

	// 登出 → cookie 失效（失败计数不动会话：phase A 的 cookie 仍有效）。
	c.json(t, http.MethodPost, "/api/logout", nil)
	if resp, _ := c.do(t, http.MethodGet, "/api/me", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("logout 后 /api/me = %d; want 401", resp.StatusCode)
	}

	// 审计三事件。login_ok 的 Output 形如 "user=admin ip=127.0.0.1"。
	var loginOK, loginFail, logoutCnt int
	for _, e := range e2e.ReadAudit(t, dataDir) {
		switch e.Tool {
		case "login_ok":
			loginOK++
			if e.User != adminUser || !strings.Contains(e.Output, "ip=") {
				t.Errorf("login_ok 条目 User=%q Output=%q; want admin 且 Output 含 ip=", e.User, e.Output)
			}
		case "login_fail":
			loginFail++
		case "logout":
			logoutCnt++
			if e.User != adminUser {
				t.Errorf("logout 条目 User = %q; want admin", e.User)
			}
		}
	}
	if loginOK == 0 {
		t.Error("审计未见 login_ok")
	}
	if loginFail < 2 {
		t.Errorf("login_fail 条数 = %d; want >=2", loginFail)
	}
	if logoutCnt == 0 {
		t.Error("审计未见 logout")
	}

	// 冷却到期恢复：新客户端正确密码回到 200。限速按用户名计（换客户端
	// 绕不开服务端状态），轮询到冷却过期；成功登录顺带清零失败计数。
	c2 := newAPIClient(p.baseURL)
	recoverDeadline := time.Now().Add(45 * time.Second)
	for {
		if time.Now().After(recoverDeadline) {
			t.Fatal("冷却过期后正确密码登录未恢复 200")
		}
		if code := c2.login(t, adminUser, adminPass); code == http.StatusOK {
			break
		} else if code != http.StatusTooManyRequests {
			t.Fatalf("正确密码登录 = %d; want 200", code)
		}
		time.Sleep(500 * time.Millisecond)
	}
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

// TestW02HostCRUD 主机管理全链路（Docker sshd 真机）：REST 建主机（密码
// 认证）→ 清单可见 → 连通测试（真实 SSH 拨号 + echo/OS 探测）→ 删除消失
// 且不波及并存主机；审计 host_save/host_test/host_delete 三事件齐备 +
// 凭据红线（ssh 密码明文绝不入任何条目的 Input/Output）。
func TestW02HostCRUD(t *testing.T) {
	hs := e2e.StartHosts(t, 1) // Docker 缺失 → Skip（同 headless 档约定）
	target := hs[0]

	dataDir := t.TempDir()
	// 预置主机走 headless 档的 e2e-pass 密码引用（server 侧无对应秘密；
	// 本用例不对它做连通测试，仅作清单并存的背景）。
	e2e.SeedHosts(t, dataDir, []e2e.SeedHost{{Name: target.Name, Addr: target.Addr, Port: target.Port}})

	stub := e2e.StartStubLLM(t, nil) // W02 不触发 LLM
	p := startServer(t, dataDir, stub.URL(), "e2e-stub-key")

	c := loginOK(t, p, adminUser, adminPass)

	const restName = "w02-rest-host"

	// 建主机：密码认证（字段对照 webapi 单测的 hostReq）。密码进 server
	// 的加密秘密库（hosts 行只留 passwordRef），清单里能看到它。
	// 凭据值与脱敏红线见包级常量 sshHostPass 的注释。
	m := c.json(t, http.MethodPost, "/api/hosts", map[string]any{
		"name": restName, "addr": target.Addr, "port": target.Port,
		"user": "root", "authMethod": "password", "password": sshHostPass,
	})
	id, _ := m["id"].(string)
	if id == "" {
		t.Fatalf("建主机返回空 id: %v", m)
	}
	if names := listHostNames(t, c); !names[restName] {
		t.Fatalf("清单未见新建主机 %s: %v", restName, names)
	}

	// 连通测试：真实 SSH（TOFU 首拨写 dataDir/known_hosts）+ echo 探测，
	// 成功回 "连通正常 · <OS>"（OS 取自 /etc/os-release）。
	tm := c.json(t, http.MethodPost, "/api/hosts/"+id+"/test", nil)
	if msg, _ := tm["message"].(string); !strings.Contains(msg, "连通正常") {
		t.Fatalf("连通测试 message = %v; want 含 连通正常", tm)
	}

	// 删除 → 新建主机消失、预置主机不受波及（不断言清单长度）
	c.json(t, http.MethodDelete, "/api/hosts/"+id, nil)
	names := listHostNames(t, c)
	if names[restName] {
		t.Fatalf("删除后清单仍含 %s: %v", restName, names)
	}
	if !names[target.Name] {
		t.Fatalf("删除波及预置主机 %s: %v", target.Name, names)
	}

	// 审计：三事件齐备（host_save/host_delete 的 Input 含 "name"，host_test
	// 的 Output 锚 "host=<名> ok"）+ 全条目脱敏（Input/Output 均不得含
	// ssh 密码明文——含 login 等无关条目一并扫，红线是全量性的）。
	var sawSave, sawTest, sawDelete bool
	for _, e := range e2e.ReadAudit(t, dataDir) {
		switch e.Tool {
		case "host_save":
			if strings.Contains(string(e.Input), `"name":"`+restName+`"`) {
				sawSave = true
			}
		case "host_test":
			if e.Output == "host="+restName+" ok" {
				sawTest = true
			}
		case "host_delete":
			if strings.Contains(string(e.Input), `"name":"`+restName+`"`) && e.Output == "host="+restName {
				sawDelete = true
			}
		}
		if strings.Contains(string(e.Input), sshHostPass) || strings.Contains(e.Output, sshHostPass) {
			t.Errorf("审计 %s 条目泄漏 ssh 密码明文: Input=%s Output=%q", e.Tool, e.Input, e.Output)
		}
	}
	if !sawSave || !sawTest || !sawDelete {
		t.Errorf("审计三事件缺失: save=%v test=%v delete=%v", sawSave, sawTest, sawDelete)
	}
}

// sshHostPass 容器 sshd 的 root 密码（同 internal/e2e 的 e2ePass；integration
// 的 sshd.Dockerfile 内置）。用真实凭据当脱敏标记最硬：它必须真的能拨通
// 容器，又绝不允许出现在审计里。
const sshHostPass = "gengar-it-pass"

// provisionExecHost 建一台可真执行的目标机（W03/W04 共用）：Docker sshd
// 容器由 StartHosts 起好，主机条目走 REST 建——SeedHosts 播的 e2e-pass
// 密码引用只在 headless 的 --secrets 注入下可解析，server 用自己的加密
// 秘密库且无注入口，execute_command 要真拨通必须经 REST 把密码写进库
// （W02 已验证的路径）。主机名沿用容器名 e2e-host-N：桩工具调用的 hosts
// 与审计的 Hosts 断言都引用它。
func provisionExecHost(t *testing.T, c *apiClient, target e2e.HostSpec) {
	t.Helper()
	m := c.json(t, http.MethodPost, "/api/hosts", map[string]any{
		"name": target.Name, "addr": target.Addr, "port": target.Port,
		"user": "root", "authMethod": "password", "password": sshHostPass,
	})
	if id, _ := m["id"].(string); id == "" {
		t.Fatalf("建执行目标主机 %s 返回空 id: %v", target.Name, m)
	}
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

// containsStr 报告 s 是否在列表里（审计 Hosts 的包含断言）。
func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// frameInt 读帧的数值字段（seq/next 等在 map 里是 float64）。
func frameInt(f map[string]any, key string) int {
	n, _ := f[key].(float64)
	return int(n)
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

// TestW03ChatConfirmFlow 对话确认流全链路（Docker sshd 真机）：WS 开会话
// → POST /api/chat 触发 Risky 工具 → confirm 帧（reqs 锚工具与目标主机）
// → REST 批准 → confirm_done → tool_start → tool_result（容器真执行，
// body 含 df 的 Filesystem 表头）→ done；桩消费两回合（工具 + 拒绝后的
// 收束文本）；审计链 chat_input → confirm_resolve → execute_command 按
// 文件序成立；第二轮纯文本消息正常 done（会话可续用、单任务锁已复位）。
//
// 命令取 "df -h; touch /tmp/gengar-w03"：df -h 单独在只读白名单（Safe
// 不弹确认），touch 段不在——分段判定下整条命令 Risky 必弹确认，同时
// df 的输出（Filesystem 表头）保留在 tool_result 里当真执行锚点。
func TestW03ChatConfirmFlow(t *testing.T) {
	hs := e2e.StartHosts(t, 1) // Docker 缺失 → Skip（同 headless 档约定）
	target := hs[0]

	dataDir := t.TempDir()
	stub := e2e.StartStubLLM(t, []e2e.Turn{
		{ToolCalls: []e2e.ToolCall{{ID: "w03t1", Name: "execute_command", Input: map[string]any{
			"command": "df -h; touch /tmp/gengar-w03", "hosts": []string{target.Name},
		}}}},
		{FinalText: "磁盘检查完成"},
		{FinalText: "总结完成"},
	})
	p := startServer(t, dataDir, stub.URL(), "e2e-stub-key")
	c := loginOK(t, p, adminUser, adminPass)
	provisionExecHost(t, c, target)

	w, sid := openChatWS(t, c)

	// 触发 → confirm 帧。reqs 是 []ui.ConfirmRequest（无 json tag，键为
	// Go 字段名 ToolName/Summary/HighRisk/Hosts）。
	c.json(t, http.MethodPost, "/api/chat", map[string]string{"text": "看磁盘", "session": sid})
	cf := w.waitEvent(t, "confirm", 15*time.Second)
	cid, _ := cf["id"].(string)
	reqs, _ := cf["reqs"].([]any)
	if cid == "" || len(reqs) == 0 {
		t.Fatalf("confirm 帧 id=%q reqs=%v", cid, reqs)
	}
	req0, _ := reqs[0].(map[string]any)
	if req0["ToolName"] != "execute_command" {
		t.Fatalf("reqs[0].ToolName = %v; want execute_command", req0["ToolName"])
	}
	if !jsonStrings(req0["Hosts"])[target.Name] {
		t.Fatalf("reqs[0].Hosts = %v; want 含 %s", req0["Hosts"], target.Name)
	}

	// REST 批准 → 终态顺序：confirm_done → tool_start → tool_result → done
	c.json(t, http.MethodPost, "/api/confirm/"+cid, map[string]any{"approved": true, "session": sid})
	if f := w.waitEvent(t, "confirm_done", 10*time.Second); f["id"] != cid {
		t.Fatalf("confirm_done id = %v; want %s", f["id"], cid)
	}
	if ev := w.waitChatEvent(t, "tool_start", 10*time.Second); ev["id"] != "w03t1" {
		t.Fatalf("tool_start id = %v; want w03t1", ev["id"])
	}
	ev := w.waitChatEvent(t, "tool_result", 30*time.Second)
	if body, _ := ev["body"].(string); !strings.Contains(body, "Filesystem") {
		t.Fatalf("tool_result body 未含 Filesystem（容器未真执行?）: %q", body)
	}
	if ev["ok"] != true {
		t.Fatalf("tool_result ok = %v; want true", ev["ok"])
	}
	w.waitChatEvent(t, "done", 10*time.Second)

	// 桩消费两回合：工具调用 + 工具结果回传后的收束文本
	if n := stub.RequestCount(); n != 2 {
		t.Fatalf("桩请求数 = %d; want 2（工具回合 + 收束文本回合）", n)
	}

	// 审计链（文件序）：chat_input → confirm_resolve → execute_command。
	// done 帧收到时任务 goroutine 的 defer 已走完（busy 复位在 EmitDone
	// 之前），全部条目已 fsync——此刻读取无竞态。
	assertAuditChain(t, e2e.ReadAudit(t, dataDir), []auditStep{
		{"chat_input", func(e audit.Entry) bool {
			return e.Session == sid && strings.Contains(string(e.Input), "看磁盘")
		}},
		{"confirm_resolve", func(e audit.Entry) bool {
			return e.Session == sid && e.Output == "decision=approve"
		}},
		{"execute_command", func(e audit.Entry) bool {
			return e.Approved && !e.IsError && containsStr(e.Hosts, target.Name)
		}},
	})

	// 第二轮：纯文本（桩第 3 回合）→ delta + done。会话未被废、单任务
	// 锁已复位（否则会收到「单任务锁」error 帧而非 delta）。
	c.json(t, http.MethodPost, "/api/chat", map[string]string{"text": "总结", "session": sid})
	if ev := w.waitChatEvent(t, "delta", 10*time.Second); !strings.Contains(fmt.Sprint(ev["text"]), "总") {
		t.Fatalf("delta text = %v; want 含 总", ev["text"])
	}
	w.waitChatEvent(t, "done", 10*time.Second)
	if n := stub.RequestCount(); n != 3 {
		t.Fatalf("第二轮后桩请求数 = %d; want 3", n)
	}
}

// TestW04DisconnectCancel 断线自动取消 + since 重连重放：confirm 挂起时
// 关 WS（服务端读循环感知断链 → unsubscribe → onIdle → CancelPendings）
// → ws_auto_cancel 审计（cancelled=1）+ execute_command 落 "user rejected"
// （agent 层通用拒绝，来源区分在 webapi）；重连同 session since=0 全量
// 重放，confirm 与 confirm_done 都在且序号递增（先弹后收，终态正确）；
// 重放收完后再发新消息正常 done（会话未被废）。
func TestW04DisconnectCancel(t *testing.T) {
	hs := e2e.StartHosts(t, 1) // Docker 缺失 → Skip（同 headless 档约定）
	target := hs[0]

	dataDir := t.TempDir()
	stub := e2e.StartStubLLM(t, []e2e.Turn{
		{ToolCalls: []e2e.ToolCall{{ID: "w04t1", Name: "execute_command", Input: map[string]any{
			"command": "df -h; touch /tmp/gengar-w04", "hosts": []string{target.Name},
		}}}},
		{FinalText: "命令未获批准"},
		{FinalText: "会话仍可用"},
	})
	p := startServer(t, dataDir, stub.URL(), "e2e-stub-key")
	c := loginOK(t, p, adminUser, adminPass)
	provisionExecHost(t, c, target)

	w, sid := openChatWS(t, c)

	c.json(t, http.MethodPost, "/api/chat", map[string]string{"text": "看磁盘", "session": sid})
	cf := w.waitEvent(t, "confirm", 15*time.Second)
	cid, _ := cf["id"].(string)
	if cid == "" {
		t.Fatalf("confirm 帧 id 为空: %v", cf)
	}

	// 断线：服务端读循环感知（TCP FIN 即时，但取消链与落盘都在服务端
	// goroutine）→ 轮询审计至两信号齐备。
	w.close()
	entries := waitAuditEntries(t, dataDir, 10*time.Second, func(es []audit.Entry) bool {
		var sawCancel, sawReject bool
		for _, e := range es {
			switch {
			case e.Tool == "ws_auto_cancel" && e.Session == sid:
				sawCancel = true
			case e.Tool == "execute_command" && e.IsError && strings.Contains(e.Output, "user rejected"):
				sawReject = true
			}
		}
		return sawCancel && sawReject
	})
	var sawCancelDetail bool
	for _, e := range entries {
		if e.Tool == "ws_auto_cancel" {
			if e.User != adminUser || e.Output != fmt.Sprintf("session=%s cancelled=1", sid) {
				t.Fatalf("ws_auto_cancel = User=%q Session=%q Output=%q", e.User, e.Session, e.Output)
			}
			sawCancelDetail = true
		}
		if e.Tool == "execute_command" && e.IsError {
			if e.Approved || !containsStr(e.Hosts, target.Name) {
				t.Fatalf("拒绝的 execute_command = Approved=%v Hosts=%v; want 未批准且含 %s", e.Approved, e.Hosts, target.Name)
			}
		}
	}
	if !sawCancelDetail {
		t.Fatalf("轮询命中后条目里无 ws_auto_cancel: %v", entries)
	}

	// 重连：同会话 since=0 全量重放。hello.session 一致（复活成新会话会
	// 换 id——那正是要排除的）；重放里 confirm 与 confirm_done 都在，
	// seq 递增（ConfirmBatch 先发布、CancelPendings 的 confirm_done 后发
	// 布，缓冲序即因果序）；扫到旧 done 收齐（重放 + 实时流兜住尚未 flush
	// 的收尾帧）。
	w2 := dialChatWS(t, c, sid, 0)
	h2 := w2.next(t, 10*time.Second)
	if h2["t"] != "hello" || h2["session"] != sid {
		t.Fatalf("重连 hello = %v; want session=%s", h2, sid)
	}
	if frameInt(h2, "next") == 0 {
		t.Fatalf("重连 hello.next = 0; want >0（断线期间有帧入缓冲）: %v", h2)
	}
	var sawConfirm, sawConfirmDone, sawOldDone bool
	var seqConfirm, seqConfirmDone int
	collectDeadline := time.Now().Add(10 * time.Second)
	for !(sawConfirm && sawConfirmDone && sawOldDone) {
		f := w2.next(t, time.Until(collectDeadline))
		switch f["t"] {
		case "confirm":
			if f["id"] == cid {
				sawConfirm, seqConfirm = true, frameInt(f, "seq")
			}
		case "confirm_done":
			if f["id"] == cid {
				sawConfirmDone, seqConfirmDone = true, frameInt(f, "seq")
			}
		case "chat":
			if ev, _ := f["ev"].(map[string]any); ev["type"] == "done" {
				sawOldDone = true
			}
		}
	}
	if !sawConfirm || !sawConfirmDone {
		t.Fatalf("重放缺帧: confirm=%v(seq=%d) confirm_done=%v(seq=%d)", sawConfirm, seqConfirm, sawConfirmDone, seqConfirmDone)
	}
	if seqConfirm >= seqConfirmDone {
		t.Fatalf("重放序错乱: confirm seq=%d 应小于 confirm_done seq=%d", seqConfirm, seqConfirmDone)
	}

	// 会话未被废：旧 done 已消费（读位在实时尾），新消息 → delta + done
	c.json(t, http.MethodPost, "/api/chat", map[string]string{"text": "会话还能用吗", "session": sid})
	w2.waitChatEvent(t, "delta", 10*time.Second)
	w2.waitChatEvent(t, "done", 10*time.Second)
	if n := stub.RequestCount(); n != 3 {
		t.Fatalf("桩请求数 = %d; want 3（工具回合 + 拒绝收束 + 新消息）", n)
	}
}
