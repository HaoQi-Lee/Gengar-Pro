//go:build e2e

package webapi

import (
	"net/http"
	"os"
	"testing"

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

// TestServerSmoke 冒烟：真实 gengar-server 子进程上走通 登录 → me
// （首用户 admin 的角色提权由 ensureAdmin 兑现）→ 登出 → 401。桩 LLM
// 用空脚本兜底（冒烟不触发对话，仅为 startServer 的 env 形位）。
func TestServerSmoke(t *testing.T) {
	dataDir := t.TempDir()
	stub := e2e.StartStubLLM(t, nil)
	p := startServer(t, dataDir, stub.URL(), "e2e-stub-key")

	c := newAPIClient(p.baseURL)
	if code := c.login(t, adminUser, adminPass); code != http.StatusOK {
		t.Fatalf("login %s = %d; want 200", adminUser, code)
	}
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
