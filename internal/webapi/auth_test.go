package webapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gengar-pro/internal/host"
)

// newTestServer 建一个带临时数据目录与测试用户的 Server。
func newTestServer(t *testing.T, users map[string]string) *Server {
	t.Helper()
	return newTestServerAt(t, users, host.MemoryStore{})
}

func doJSON(t *testing.T, s *Server, method, path, body, cookie string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var rdr *strings.Reader
	if body == "" {
		rdr = strings.NewReader("")
	} else {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w, m
}

func TestLoginFlow(t *testing.T) {
	s := newTestServer(t, map[string]string{"alice": "pw1"})

	// 未登录访问受保护端点 → 401
	w, _ := doJSON(t, s, "GET", "/api/me", "", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("me without login = %d; want 401", w.Code)
	}
	// 错密码 → 401（触发限速计数；下步正确登录前显式清零——限速
	// 语义验证见 TestLoginRateLimit）
	w, _ = doJSON(t, s, "POST", "/api/login", `{"username":"alice","password":"bad"}`, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password = %d; want 401", w.Code)
	}
	s.loginLim.success("alice")
	// 正确登录 → 200 + Set-Cookie
	w, m := doJSON(t, s, "POST", "/api/login", `{"username":"alice","password":"pw1"}`, "")
	if w.Code != http.StatusOK || m["username"] != "alice" {
		t.Fatalf("login = %d, %v", w.Code, m)
	}
	cookie := w.Header().Get("Set-Cookie")
	if !strings.Contains(cookie, "gengar_sess=") || !strings.Contains(cookie, "HttpOnly") || !strings.Contains(cookie, "SameSite=Lax") {
		t.Fatalf("cookie = %q", cookie)
	}
	// 带 cookie 访问 /api/me → 200
	tok := strings.SplitN(strings.Split(cookie, ";")[0], "=", 2)[1]
	w, m = doJSON(t, s, "GET", "/api/me", "", "gengar_sess="+tok)
	if w.Code != http.StatusOK || m["username"] != "alice" {
		t.Fatalf("me = %d, %v", w.Code, m)
	}
	// 登出后原 token 失效
	w, _ = doJSON(t, s, "POST", "/api/logout", "", "gengar_sess="+tok)
	if w.Code != http.StatusOK {
		t.Fatalf("logout = %d", w.Code)
	}
	w, _ = doJSON(t, s, "GET", "/api/me", "", "gengar_sess="+tok)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("me after logout = %d; want 401", w.Code)
	}
}

func TestLoginBadJSON(t *testing.T) {
	s := newTestServer(t, map[string]string{"alice": "pw1"})
	w, _ := doJSON(t, s, "POST", "/api/login", `{not json`, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad json = %d; want 400", w.Code)
	}
}

// TestLoginRateLimit: 评审 ③-1 登录失败指数限速——同一用户名连续失败后，
// 冷却窗口内拒绝尝试（429）；窗口长短到极限值。成功登录清零计数。
func TestLoginRateLimit(t *testing.T) {
	s := newTestServer(t, map[string]string{"alice": "pw1"})
	// 第一次失败：401 + 计 1（冷却 2s）
	w, _ := doJSON(t, s, "POST", "/api/login", `{"username":"alice","password":"bad"}`, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("first failure = %d; want 401", w.Code)
	}
	// 冷却窗口内再试（密码对也拒）：429
	w, _ = doJSON(t, s, "POST", "/api/login", `{"username":"alice","password":"pw1"}`, "")
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("cooldown window = %d; want 429", w.Code)
	}
	// 时间穿越：冷却到期后放行（直接改 nextTry 避免真实等待）
	s.loginLim.mu.Lock()
	s.loginLim.nextTry["alice"] = time.Now().Add(-time.Second)
	s.loginLim.mu.Unlock()
	w, m := doJSON(t, s, "POST", "/api/login", `{"username":"alice","password":"pw1"}`, "")
	if w.Code != http.StatusOK || m["username"] != "alice" {
		t.Fatalf("after cooldown = %d %v; want 200", w.Code, m)
	}
	// 成功清零：失败一次后立即（窗口极短）可再成功——重置计数验证
	s.loginLim.mu.Lock()
	s.loginLim.failures["alice"] = 1
	s.loginLim.nextTry["alice"] = time.Now().Add(-time.Second) // 窗口已过
	s.loginLim.mu.Unlock()
	w, _ = doJSON(t, s, "POST", "/api/login", `{"username":"alice","password":"bad"}`, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("second failure = %d; want 401", w.Code)
	}
	// success 清零（这是被测行为）：success 后再失败应从 1 起计而非叠 2
	s.loginLim.success("alice")
	s.loginLim.mu.Lock()
	if s.loginLim.failures["alice"] != 0 {
		t.Fatalf("success must reset failures, got %d", s.loginLim.failures["alice"])
	}
	s.loginLim.mu.Unlock()
}

func TestMeAndLoginReturnRole(t *testing.T) {
	s := newTestServer(t, map[string]string{"alice": "pw1", "root": "pw2"})
	if err := s.core.Store().SetUserRole("root", "admin"); err != nil {
		t.Fatalf("SetUserRole: %v", err)
	}
	w, m := doJSON(t, s, "GET", "/api/me", "", login(t, s, "root", "pw2"))
	if w.Code != http.StatusOK || m["role"] != "admin" {
		t.Fatalf("me(root) = %d, %v; want role admin", w.Code, m)
	}
	w, m = doJSON(t, s, "GET", "/api/me", "", login(t, s, "alice", "pw1"))
	if w.Code != http.StatusOK || m["role"] != "" {
		t.Fatalf("me(alice) = %d, %v; want role empty", w.Code, m)
	}
	// 登录响应也带 role（前端登录后无需再探一次 me）
	w, m = doJSON(t, s, "POST", "/api/login", `{"username":"root","password":"pw2"}`, "")
	if w.Code != http.StatusOK || m["role"] != "admin" {
		t.Fatalf("login(root) = %d, %v; want role admin", w.Code, m)
	}
}
