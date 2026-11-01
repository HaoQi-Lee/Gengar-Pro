//go:build e2e

package webapi

import (
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

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

	const (
		restName = "w02-rest-host"
		// 容器账号密码同 internal/e2e 的 e2ePass（integration 的
		// sshd.Dockerfile 内置 root:gengar-it-pass）。用真实凭据当脱敏
		// 标记最硬：它必须真的能拨通容器，又绝不允许出现在审计里。
		sshPass = "gengar-it-pass"
	)

	// 建主机：密码认证（字段对照 webapi 单测的 hostReq）。密码进 server
	// 的加密秘密库（hosts 行只留 passwordRef），清单里能看到它。
	m := c.json(t, http.MethodPost, "/api/hosts", map[string]any{
		"name": restName, "addr": target.Addr, "port": target.Port,
		"user": "root", "authMethod": "password", "password": sshPass,
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
		if strings.Contains(string(e.Input), sshPass) || strings.Contains(e.Output, sshPass) {
			t.Errorf("审计 %s 条目泄漏 ssh 密码明文: Input=%s Output=%q", e.Tool, e.Input, e.Output)
		}
	}
	if !sawSave || !sawTest || !sawDelete {
		t.Errorf("审计三事件缺失: save=%v test=%v delete=%v", sawSave, sawTest, sawDelete)
	}
}
