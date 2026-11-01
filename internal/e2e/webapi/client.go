//go:build e2e

package webapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// apiClient 带 cookie jar 的 REST 客户端：登录后 gengar_sess cookie 由
// jar 自动随请求携带。do 是底层请求（不判状态码），login/json 是其上
// 的便捷断言层。
type apiClient struct {
	http *http.Client
	base string
}

func newAPIClient(base string) *apiClient {
	jar, _ := cookiejar.New(nil) // nil options 恒不报错
	return &apiClient{http: &http.Client{Jar: jar}, base: base}
}

// do 发 JSON 请求：body 为 nil 不带请求体，否则 json.Marshal。返回原始
// 响应（Body 已读尽关闭）与解析后的 JSON 对象；空 body / 非 JSON 时 map
// 为 nil 不报错——状态码路径（401/404/429）的调用方只看 resp。
func (c *apiClient) do(t *testing.T, method, path string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal %s %s body: %v", method, path, err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		t.Fatalf("new request %s %s: %v", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s %s body: %v", method, path, err)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp, m
}

// login POST /api/login，返回状态码（不 Fatal——错误密码 401、限速 429
// 正是 W01 的断言对象）。
func (c *apiClient) login(t *testing.T, user, pass string) int {
	t.Helper()
	resp, _ := c.do(t, http.MethodPost, "/api/login", map[string]string{
		"username": user, "password": pass,
	})
	return resp.StatusCode
}

// json 断言 200 并返回解析后的 body；非 200 直接 Fatal（错误响应走 do
// 拿状态码，别用这个）。
func (c *apiClient) json(t *testing.T, method, path string, body any) map[string]any {
	t.Helper()
	resp, m := c.do(t, method, path, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %s = %d: %v", method, path, resp.StatusCode, m)
	}
	return m
}

// wsClient 对话 WS 客户端。读是同步拉模式（next/waitEvent），无后台
// goroutine——W 用例都是顺序收帧断言；waitEvent 内部跳读不匹配帧。
type wsClient struct {
	conn   *websocket.Conn
	closed chan struct{}
}

// dialChatWS 拨 ws://<base>/api/ws/chat?since=N&session=ID（session 空 =
// 服务端新建，hello 帧回实际 id）。
//
// cookie 手工经 requestHeader 带：net/http/cookiejar 对非 http(s) scheme
// 的 URL 一律返回空集，gorilla Dialer.Jar 拿 ws:// URL 取 jar 是空操作
// ——必须先以 http scheme 镜像 URL 从 jar 取出再显式设置。无 Origin 头
// 的非浏览器客户端被服务端 CheckOrigin 放行（chat.go 注释明示）。
func dialChatWS(t *testing.T, c *apiClient, session string, since int) *wsClient {
	t.Helper()
	u := strings.Replace(c.base, "http://", "ws://", 1) +
		"/api/ws/chat?since=" + fmt.Sprint(since)
	if session != "" {
		u += "&session=" + session
	}
	hdr := http.Header{}
	if c.http.Jar != nil {
		httpURL, err := url.Parse(c.base)
		if err != nil {
			t.Fatalf("parse base %s: %v", c.base, err)
		}
		var parts []string
		for _, ck := range c.http.Jar.Cookies(httpURL) {
			parts = append(parts, ck.Name+"="+ck.Value)
		}
		if len(parts) > 0 {
			hdr.Set("Cookie", strings.Join(parts, "; "))
		}
	}
	conn, _, err := (&websocket.Dialer{HandshakeTimeout: 10 * time.Second}).Dial(u, hdr)
	if err != nil {
		t.Fatalf("ws dial %s: %v", u, err)
	}
	w := &wsClient{conn: conn, closed: make(chan struct{})}
	t.Cleanup(w.close)
	return w
}

// next 读下一帧（单帧读超时）。帧形态 map[string]any，键对照 webapi
// wsFrame 的 json tag：t/seq/next/session/scope/ev/id/reqs/input。
func (w *wsClient) next(t *testing.T, timeout time.Duration) map[string]any {
	t.Helper()
	if err := w.conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	var f map[string]any
	if err := w.conn.ReadJSON(&f); err != nil {
		t.Fatalf("read ws frame: %v", err)
	}
	return f
}

// waitEvent 跳读至 t==typ 的帧并返回（中途帧丢弃，已见帧型进超时报错
// 辅助诊断）。timeout 是总预算而非单帧超时。服务端语义 hello 必为首
// 帧，需要它时先单独 next 一次。
func (w *wsClient) waitEvent(t *testing.T, typ string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var seen []string
	for {
		remain := time.Until(deadline)
		if remain <= 0 {
			t.Fatalf("waitEvent(%s) 超时（%v），已见帧: %v", typ, timeout, seen)
		}
		f := w.next(t, remain)
		if f["t"] == typ {
			return f
		}
		seen = append(seen, fmt.Sprint(f["t"]))
	}
}

// waitChatEvent 跳读至 t="chat" 且 ev.type==evType 的帧，返回 ev 的 map
// （键对照 hub.go chatEvent 的 json tag：type/id/text/display/body/ok…）。
// tool_start/tool_result/done 等对话事件不是帧类型——它们都在 chat 帧内
// （hub.publishChat 包一层 T:"chat"），waitEvent 够不到，必须按 ev.type
// 匹配。timeout 是总预算；中途帧丢弃，已见帧型进超时报错辅助诊断。
func (w *wsClient) waitChatEvent(t *testing.T, evType string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var seen []string
	for {
		remain := time.Until(deadline)
		if remain <= 0 {
			t.Fatalf("waitChatEvent(%s) 超时（%v），已见帧: %v", evType, timeout, seen)
		}
		f := w.next(t, remain)
		if f["t"] != "chat" {
			seen = append(seen, fmt.Sprint(f["t"]))
			continue
		}
		ev, _ := f["ev"].(map[string]any)
		if ev["type"] == evType {
			return ev
		}
		seen = append(seen, "chat:"+fmt.Sprint(ev["type"]))
	}
}

// close 关连接，幂等（t.Cleanup 挂过一次，用例内也可显式关——W04 的
// 断线触发器就是它）；closed 通道供需要等关闭语义的用例 select。
func (w *wsClient) close() {
	select {
	case <-w.closed:
		return
	default:
	}
	close(w.closed)
	_ = w.conn.Close()
}
