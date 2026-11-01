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

// TestLoginAuditEvents 认证审计：login_ok/login_fail/logout 三类事件
// 的字段约定（login_fail 因未认证 User 置空，尝试的用户名放 Output）；
// 限速 429 路径不产生事件（无新条目）。IP 用显式 RemoteAddr 断言。
func TestLoginAuditEvents(t *testing.T) {
	s := newTestServer(t, map[string]string{"alice": "pw1"})
	l := openTestAudit(t, s)
	const ip = "203.0.113.7:5555" // TEST-NET-3，避免依赖 httptest 默认值

	// ① 正确凭据 → 200 + 1 条 login_ok
	w := doJSONFromIP(t, s, "POST", "/api/login", `{"username":"alice","password":"pw1"}`, "", ip)
	if w.Code != http.StatusOK {
		t.Fatalf("login ok = %d; want 200", w.Code)
	}
	ck := "gengar_sess=" + strings.SplitN(strings.Split(w.Header().Get("Set-Cookie"), ";")[0], "=", 2)[1]
	es := readAuditEntries(t, l)
	if len(es) != 1 || es[0].Tool != "login_ok" || es[0].User != "alice" {
		t.Fatalf("login_ok 事件 = %+v; want 1 条 tool=login_ok user=alice", es)
	}
	if es[0].Output != "user=alice ip=203.0.113.7" || es[0].TS == "" {
		t.Fatalf("login_ok 字段 = %+v", es[0])
	}

	// ② 登出 → 1 条 logout：User=会话用户名
	if w, _ := doJSON(t, s, "POST", "/api/logout", "", ck); w.Code != http.StatusOK {
		t.Fatalf("logout = %d; want 200", w.Code)
	}
	es = readAuditEntries(t, l)
	if len(es) != 2 || es[1].Tool != "logout" || es[1].User != "alice" || es[1].Output != "user=alice" {
		t.Fatalf("logout 事件 = %+v; want tool=logout user=alice", es)
	}

	// ③ 错密码 → 401 + 1 条 login_fail：未认证 User 空，尝试名在 Output
	if w := doJSONFromIP(t, s, "POST", "/api/login", `{"username":"alice","password":"bad"}`, "", ip); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password = %d; want 401", w.Code)
	}
	es = readAuditEntries(t, l)
	if len(es) != 3 || es[2].Tool != "login_fail" || es[2].User != "" {
		t.Fatalf("login_fail(错密码) = %+v; want tool=login_fail user 空", es)
	}
	if es[2].Output != "user=alice ip=203.0.113.7" {
		t.Fatalf("login_fail(错密码) Output = %q", es[2].Output)
	}

	// ④ 不存在的用户名 → 同样 1 条 login_fail（Output 带尝试名）
	if w := doJSONFromIP(t, s, "POST", "/api/login", `{"username":"ghost","password":"bad"}`, "", ip); w.Code != http.StatusUnauthorized {
		t.Fatalf("no such user = %d; want 401", w.Code)
	}
	es = readAuditEntries(t, l)
	if len(es) != 4 || es[3].Tool != "login_fail" || es[3].User != "" || es[3].Output != "user=ghost ip=203.0.113.7" {
		t.Fatalf("login_fail(无此用户) = %+v", es)
	}

	// ⑤ 限速 429：alice ③ 中已失败一次（冷却 2s），立刻再试被拒 → 无新条目
	if w := doJSONFromIP(t, s, "POST", "/api/login", `{"username":"alice","password":"pw1"}`, "", ip); w.Code != http.StatusTooManyRequests {
		t.Fatalf("rate limited = %d; want 429", w.Code)
	}
	if es = readAuditEntries(t, l); len(es) != 4 {
		t.Fatalf("429 后条数 = %d; want 4（限速拒绝不记事件）", len(es))
	}
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
