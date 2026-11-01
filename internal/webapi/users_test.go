package webapi

import (
	"net/http"
	"strings"
	"testing"

	"gengar-pro/internal/host"
)

// newAdminServer 建带 admin（已提权）与普通用户 alice 的 server。
func newAdminServer(t *testing.T) *Server {
	t.Helper()
	s := newTestServer(t, map[string]string{"admin": "adminpw", "alice": "pw1"})
	if err := s.core.Store().SetUserRole("admin", "admin"); err != nil {
		t.Fatalf("SetUserRole: %v", err)
	}
	return s
}

func TestUsersRequireAdmin(t *testing.T) {
	s := newAdminServer(t)
	w, _ := doJSON(t, s, "GET", "/api/users", "", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("未登录 = %d; want 401", w.Code)
	}
	w, _ = doJSON(t, s, "GET", "/api/users", "", login(t, s, "alice", "pw1"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("非 admin = %d; want 403", w.Code)
	}
	w, m := doJSON(t, s, "GET", "/api/users", "", login(t, s, "admin", "adminpw"))
	if w.Code != http.StatusOK {
		t.Fatalf("admin = %d; want 200", w.Code)
	}
	if _, ok := m["users"]; !ok {
		t.Fatalf("响应缺 users: %v", m)
	}
}

func TestUserLifecycleByAdmin(t *testing.T) {
	s := newAdminServer(t)
	ck := login(t, s, "admin", "adminpw")

	w, _ := doJSON(t, s, "POST", "/api/users", `{"username":"bob","password":"bobpw123"}`, ck)
	if w.Code != http.StatusOK {
		t.Fatalf("create = %d; want 200", w.Code)
	}
	w, m := doJSON(t, s, "POST", "/api/users", `{"username":"bob","password":"bobpw123"}`, ck)
	if w.Code != http.StatusBadRequest || m["error"] != "用户名已存在" {
		t.Fatalf("重名 = %d, %v; want 400 用户名已存在", w.Code, m)
	}
	bobCk := login(t, s, "bob", "bobpw123")

	// 重置密码 → bob 旧会话吊销；旧密码失效、新密码可登录
	w, _ = doJSON(t, s, "PUT", "/api/users/bob/password", `{"password":"newpw456"}`, ck)
	if w.Code != http.StatusOK {
		t.Fatalf("reset = %d; want 200", w.Code)
	}
	w, _ = doJSON(t, s, "GET", "/api/me", "", bobCk)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("bob 旧会话 = %d; want 401", w.Code)
	}
	w, _ = doJSON(t, s, "POST", "/api/login", `{"username":"bob","password":"bobpw123"}`, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("旧密码登录 = %d; want 401", w.Code)
	}
	// 旧密码失败触发限速冷却（fail 计数 2）——绕过等待直接验证计数存在，
	// 然后用成功路径验证 success 清零。冷却窗口内旧密码再试必然 429 或 401，
	// 断言 429 会与时序耦合，改为显式清零后登录。
	s.loginLim.success("bob")
	login(t, s, "bob", "newpw456")

	// 删除 → bob 一切登录失败；再删 404
	w, _ = doJSON(t, s, "DELETE", "/api/users/bob", "", ck)
	if w.Code != http.StatusOK {
		t.Fatalf("delete = %d; want 200", w.Code)
	}
	w, _ = doJSON(t, s, "POST", "/api/login", `{"username":"bob","password":"newpw456"}`, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("删除后登录 = %d; want 401", w.Code)
	}
	w, _ = doJSON(t, s, "DELETE", "/api/users/bob", "", ck)
	if w.Code != http.StatusNotFound {
		t.Fatalf("删除不存在 = %d; want 404", w.Code)
	}
}

func TestCreateUserValidation(t *testing.T) {
	s := newAdminServer(t)
	ck := login(t, s, "admin", "adminpw")
	cases := []struct{ name, body string }{
		{"非法字符", `{"username":"张三","password":"123456"}`},
		{"过长", `{"username":"` + strings.Repeat("a", 33) + `","password":"123456"}`},
		{"空白用户名", `{"username":"  ","password":"123456"}`},
		{"短密码", `{"username":"bob","password":"12345"}`},
	}
	for _, c := range cases {
		w, _ := doJSON(t, s, "POST", "/api/users", c.body, ck)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d; want 400", c.name, w.Code)
		}
	}
}

// TestCreateUserAuditOperator user_create 审计必须带操作者：admined 已
// 注入 username，但 handler 曾用 _ 丢弃，落审计时 User 恒空，无法追溯
// 是谁建的账号。登录成功后再开审计，只断言 user_create 这一条。
func TestCreateUserAuditOperator(t *testing.T) {
	s := newAdminServer(t)
	ck := login(t, s, "admin", "adminpw")
	l := openTestAudit(t, s)

	w, _ := doJSON(t, s, "POST", "/api/users", `{"username":"bob","password":"bobpw123"}`, ck)
	if w.Code != http.StatusOK {
		t.Fatalf("create = %d; want 200", w.Code)
	}
	es := readAuditEntries(t, l)
	if len(es) != 1 {
		t.Fatalf("条数 = %d; want 1", len(es))
	}
	if e := es[0]; e.Tool != "user_create" || e.User != "admin" || e.Output != "user=bob" {
		t.Fatalf("user_create 事件 = %+v; want tool=user_create user=admin output=user=bob", e)
	}
}

func TestDeleteGuards(t *testing.T) {
	s := newAdminServer(t)
	ck := login(t, s, "admin", "adminpw")
	// 不能删自己（单 admin 场景等价于最后管理员保护）
	w, _ := doJSON(t, s, "DELETE", "/api/users/admin", "", ck)
	if w.Code != http.StatusForbidden {
		t.Fatalf("自删 = %d; want 403", w.Code)
	}
	// 存在第二个 admin 时可删另一个
	if err := s.core.Store().CreateUser(host.User{Username: "root2", PwHash: "x", CreatedAt: "t", Role: "admin"}); err != nil {
		t.Fatalf("seed root2: %v", err)
	}
	w, _ = doJSON(t, s, "DELETE", "/api/users/root2", "", ck)
	if w.Code != http.StatusOK {
		t.Fatalf("删另一 admin = %d; want 200", w.Code)
	}
}
