//go:build e2e

package webapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// 假 REST+WS 服务器：模拟 gengar-server 的登录（发 cookie）、鉴权
// （cookie 校验）与 chat WS（鉴权后 upgrade、按序发帧）。cookie 记录
// 进 lastCookie 供 jar 行为断言。

type fakeServer struct {
	*httptest.Server
	mu         sync.Mutex
	lastCookie string // 最近一次 /api/me 或 WS 握手携带的 Cookie 头
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Username == "admin" && req.Password == "pw" {
			http.SetCookie(w, &http.Cookie{Name: "gengar_sess", Value: "tok-1", Path: "/"})
			writeFakeJSON(w, 200, map[string]string{"username": "admin", "role": "admin"})
			return
		}
		writeFakeJSON(w, 401, map[string]string{"error": "用户名或密码错误"})
	})
	mux.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.lastCookie = r.Header.Get("Cookie")
		f.mu.Unlock()
		if r.Header.Get("Cookie") != "gengar_sess=tok-1" {
			writeFakeJSON(w, 401, map[string]string{"error": "未登录"})
			return
		}
		writeFakeJSON(w, 200, map[string]string{"username": "admin", "role": "admin"})
	})
	mux.HandleFunc("GET /badjson", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("plain text, not json"))
	})
	mux.HandleFunc("GET /api/ws/chat", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.lastCookie = r.Header.Get("Cookie")
		f.mu.Unlock()
		if r.Header.Get("Cookie") != "gengar_sess=tok-1" {
			writeFakeJSON(w, 401, map[string]string{"error": "未登录"})
			return
		}
		conn, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		// 模拟服务端首帧 hello + 干扰帧 chat + 目标帧 confirm。
		for _, fr := range []map[string]any{
			{"t": "hello", "session": "s-1", "next": 3, "scope": ""},
			{"t": "chat", "seq": 1, "ev": map[string]any{"type": "text", "text": "干扰"}},
			{"t": "chat", "seq": 2, "ev": map[string]any{"type": "text", "text": "干扰2"}},
			{"t": "confirm", "seq": 3, "id": "c-9", "reqs": []any{
				map[string]any{"tool": "execute_command", "command": "df -h"},
			}},
		} {
			if err := conn.WriteJSON(fr); err != nil {
				return
			}
		}
		// 帧发完保持连接（客户端读完即断言，不留半开等待）。
		_, _, _ = conn.ReadMessage()
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Server.Close)
	return f
}

func writeFakeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeServer) cookie() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastCookie
}

// TestAPIClientLoginCookieFlow：登录 200 → cookie 入 jar 随后请求自动带
// （服务器侧看到 gengar_sess=tok-1）→ /api/me json() 200 拿到 role。
func TestAPIClientLoginCookieFlow(t *testing.T) {
	f := newFakeServer(t)
	c := newAPIClient(f.URL)

	if code := c.login(t, "admin", "pw"); code != 200 {
		t.Fatalf("login = %d; want 200", code)
	}
	me := c.json(t, http.MethodGet, "/api/me", nil)
	if me["role"] != "admin" || me["username"] != "admin" {
		t.Fatalf("/api/me = %v", me)
	}
	if ck := f.cookie(); ck != "gengar_sess=tok-1" {
		t.Fatalf("服务器见到的 Cookie = %q; want gengar_sess=tok-1", ck)
	}
}

// TestAPIClientLoginWrongPass：错误密码返回 401（login 不 Fatal，状态码
// 是断言对象），且无 cookie 入 jar。
func TestAPIClientLoginWrongPass(t *testing.T) {
	f := newFakeServer(t)
	c := newAPIClient(f.URL)

	if code := c.login(t, "admin", "wrong"); code != 401 {
		t.Fatalf("login wrong pass = %d; want 401", code)
	}
	// 未登录的 /api/me 走 do 拿状态码（json 会 Fatal，401 是预期）。
	resp, m := c.do(t, http.MethodGet, "/api/me", nil)
	if resp.StatusCode != 401 {
		t.Fatalf("/api/me = %d; want 401", resp.StatusCode)
	}
	if m == nil || m["error"] == "" {
		t.Fatalf("401 body = %v; want error 字段", m)
	}
}

// TestAPIClientDoBadJSON：非 JSON 响应体 → map 为 nil 且不 panic（状态
// 码仍可用）；logout 响应等空 body 场景同理不炸。
func TestAPIClientDoBadJSON(t *testing.T) {
	f := newFakeServer(t)
	c := newAPIClient(f.URL)

	resp, m := c.do(t, http.MethodGet, "/badjson", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("/badjson = %d", resp.StatusCode)
	}
	if m != nil {
		t.Fatalf("非 JSON 响应解析结果 = %v; want nil", m)
	}

	// json() 的非 200 Fatal 分支不做无进程单测（Goexit 无法在用例内安全
	// 捕获）：错误响应语义已在上面 do/login 路径覆盖，json 200 路径由
	// 登录用例与 TestServerSmoke 覆盖。
}

// TestWSClientCookieAndFrames：登录后 WS 握手必须带上 jar 里的 cookie
// （cookiejar 对 ws:// scheme 返回空集——dialChatWS 的手工 Cookie 头正是
// 为此），hello 首帧 + waitEvent 跳读 confirm（reqs 结构完整保留）。
func TestWSClientCookieAndFrames(t *testing.T) {
	f := newFakeServer(t)
	c := newAPIClient(f.URL)
	if code := c.login(t, "admin", "pw"); code != 200 {
		t.Fatalf("login = %d", code)
	}

	w := dialChatWS(t, c, "", 0)
	hello := w.next(t, 3*time.Second)
	if hello["t"] != "hello" || hello["session"] != "s-1" {
		t.Fatalf("hello = %v", hello)
	}
	// 握手 cookie 在读帧前就该被服务器记录。
	if ck := f.cookie(); !strings.Contains(ck, "gengar_sess=tok-1") {
		t.Fatalf("WS 握手 Cookie = %q; want 含 gengar_sess", ck)
	}
	confirm := w.waitEvent(t, "confirm", 3*time.Second) // 跳过两个 chat 帧
	if confirm["id"] != "c-9" {
		t.Fatalf("confirm.id = %v", confirm["id"])
	}
	reqs, ok := confirm["reqs"].([]any)
	if !ok || len(reqs) != 1 {
		t.Fatalf("confirm.reqs = %v", confirm["reqs"])
	}
	if reqs[0].(map[string]any)["tool"] != "execute_command" {
		t.Fatalf("reqs[0] = %v", reqs[0])
	}
	w.close()
	w.close() // 幂等
}
