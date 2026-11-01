package webapi

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/ssh"

	"gengar-pro/internal/appcore"
	"gengar-pro/internal/host"
)

// newTestServerAt 同 newTestServer，但允许注入自定义 SecretStore
// （模拟钥匙串故障，验证服务端错误映射 500 而非 400）。
func newTestServerAt(t *testing.T, users map[string]string, secrets host.SecretStore) *Server {
	t.Helper()
	c, err := appcore.OpenAt(t.TempDir(), secrets, 30*time.Second)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	s := New(c, nil, nil)
	s.SetKeysDir(filepath.Join(t.TempDir(), "keys"))
	for name, pw := range users {
		h, _ := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
		u := host.User{Username: name, PwHash: string(h), CreatedAt: "test"}
		if name == "admin" {
			u.Role = "admin" // 与生产 ensureAdmin 一致：admin 用户名即 admin 角色
		}
		if err := c.Store().CreateUser(u); err != nil {
			t.Fatalf("CreateUser %s: %v", name, err)
		}
	}
	return s
}

// failingSecrets 强制 Set 失败，其余行为同内存实现。
type failingSecrets struct{ host.MemoryStore }

func (failingSecrets) Set(ref, secret string) error { return errors.New("keychain down") }

// login 登录并返回可直接塞进 Cookie 头的 "gengar_sess=<tok>"。
func login(t *testing.T, s *Server, user, pw string) string {
	t.Helper()
	w, _ := doJSON(t, s, "POST", "/api/login", fmt.Sprintf(`{"username":%q,"password":%q}`, user, pw), "")
	if w.Code != http.StatusOK {
		t.Fatalf("login %s = %d", user, w.Code)
	}
	// Set-Cookie 形如 "gengar_sess=<tok>; Path=/; HttpOnly; SameSite=Lax"
	for _, part := range strings.Split(w.Header().Get("Set-Cookie"), ";") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(part), "gengar_sess="); ok {
			return "gengar_sess=" + v
		}
	}
	t.Fatal("Set-Cookie 中无 gengar_sess")
	return ""
}

func TestHostsCRUD(t *testing.T) {
	s := newTestServer(t, map[string]string{"alice": "pw1"})
	ck := login(t, s, "alice", "pw1")

	// 空清单
	w, m := doJSON(t, s, "GET", "/api/hosts", "", ck)
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d", w.Code)
	}
	if _, ok := m["hosts"]; !ok {
		t.Fatalf("list body missing hosts: %v", m)
	}

	// 新建（密码走 SecretStore，不落 hosts 行）
	body := `{"name":"web1","addr":"10.0.0.1","user":"root","tags":["web"],"authMethod":"password","password":"s3cret"}`
	w, m = doJSON(t, s, "POST", "/api/hosts", body, ck)
	if w.Code != http.StatusOK {
		t.Fatalf("create = %d, %v", w.Code, m)
	}
	id, _ := m["id"].(string)
	if id == "" {
		t.Fatalf("create returned empty id: %v", m)
	}

	// 列表带新主机，status unknown
	w, m = doJSON(t, s, "GET", "/api/hosts", "", ck)
	hosts, _ := m["hosts"].([]any)
	if len(hosts) != 1 {
		t.Fatalf("hosts len = %d; want 1", len(hosts))
	}
	h0 := hosts[0].(map[string]any)
	if h0["name"] != "web1" || h0["status"] != "unknown" {
		t.Fatalf("host[0] = %v", h0)
	}

	// 缺必填 → 400
	w, _ = doJSON(t, s, "POST", "/api/hosts", `{"name":"","addr":"a","user":"u"}`, ck)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing fields = %d; want 400", w.Code)
	}

	// 编辑（改端口），密码留空保持原值（passwordRef 不变）
	w, m = doJSON(t, s, "GET", "/api/hosts", "", ck)
	h0 = m["hosts"].([]any)[0].(map[string]any)
	oldRef, _ := h0["passwordRef"].(string)
	if oldRef == "" {
		t.Fatal("passwordRef should be set after create with password")
	}
	edit := fmt.Sprintf(`{"id":%q,"name":"web1","addr":"10.0.0.1","port":2222,"user":"root","tags":["web"],"authMethod":"password"}`, id)
	w, _ = doJSON(t, s, "POST", "/api/hosts", edit, ck)
	if w.Code != http.StatusOK {
		t.Fatalf("edit = %d", w.Code)
	}
	w, m = doJSON(t, s, "GET", "/api/hosts", "", ck)
	h0 = m["hosts"].([]any)[0].(map[string]any)
	if h0["port"].(float64) != 2222 || h0["passwordRef"] != oldRef {
		t.Fatalf("after edit = %v", h0)
	}

	// 删除 → 清单空
	w, _ = doJSON(t, s, "DELETE", "/api/hosts/"+id, "", ck)
	if w.Code != http.StatusOK {
		t.Fatalf("delete = %d", w.Code)
	}
	w, m = doJSON(t, s, "GET", "/api/hosts", "", ck)
	if n := len(m["hosts"].([]any)); n != 0 {
		t.Fatalf("after delete hosts = %d; want 0", n)
	}
}

func TestHostsRequireAuth(t *testing.T) {
	s := newTestServer(t, nil)
	w, _ := doJSON(t, s, "GET", "/api/hosts", "", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("hosts without login = %d", w.Code)
	}
}

// scope 设定 + hello 回读 + 会话隔离（对话级目标取代账号级钉住）。
func TestChatScope(t *testing.T) {
	s := newTestServer(t, map[string]string{"alice": "pw1"})
	hostAddr := startHTTP(t, s)
	ck := login(t, s, "alice", "pw1")
	_, m := doJSON(t, s, "POST", "/api/hosts", `{"name":"web1","addr":"10.0.0.1","user":"root","authMethod":"password"}`, ck)
	id0, _ := m["id"].(string)
	doJSON(t, s, "POST", "/api/hosts", `{"name":"db1","addr":"10.0.0.2","user":"root","authMethod":"password"}`, ck)

	_, read, h := wsChatOpen(t, hostAddr, ck, 0, "")
	sid := h.Session
	if h.Scope != "" {
		t.Fatalf("新会话 scope = %q; want 空", h.Scope)
	}
	// 设定目标（含一个不存在的主机 id：应被过滤）
	w, _ := doJSON(t, s, "PUT", "/api/chat/scope",
		`{"session":"`+sid+`","hostIds":["`+id0+`","ghost"]}`, ck)
	if w.Code != http.StatusOK {
		t.Fatalf("set scope = %d", w.Code)
	}
	if got := s.scopeNames(s.chatFor(sid)); got != "web1" {
		t.Fatalf("scopeNames = %q; want web1（ghost 被过滤）", got)
	}
	// 会话流回显
	sawNote := false
	for i := 0; i < 10 && !sawNote; i++ {
		f := read()
		if f.T == "chat" && f.Ev != nil && strings.Contains(f.Ev.Text, "本次对话目标已设定") && strings.Contains(f.Ev.Text, "web1") {
			sawNote = true
		}
	}
	if !sawNote {
		t.Fatal("目标设定未回显到会话")
	}
	// 重连 hello 携带 scope（刷新恢复显示的来源）
	_, _, h2 := wsChatOpen(t, hostAddr, ck, 0, sid)
	if h2.Scope != "web1" {
		t.Fatalf("reconnect hello scope = %q; want web1", h2.Scope)
	}
	// 未知会话 400
	w, _ = doJSON(t, s, "PUT", "/api/chat/scope", `{"session":"nope","hostIds":[]}`, ck)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown session scope = %d; want 400", w.Code)
	}
	// 清空目标
	doJSON(t, s, "PUT", "/api/chat/scope", `{"session":"`+sid+`","hostIds":[]}`, ck)
	if got := s.scopeNames(s.chatFor(sid)); got != "" {
		t.Fatalf("清空后 scopeNames = %q; want 空", got)
	}
}

func TestSettingsMask(t *testing.T) {
	s := newTestServer(t, map[string]string{"admin": "pw-admin", "alice": "pw1"})
	ck := login(t, s, "admin", "pw-admin")
	// 非 admin 读写设置 → 403（安全评审 P0-2：key 是全团队凭据）
	ckUser := login(t, s, "alice", "pw1")
	w, _ := doJSON(t, s, "PUT", "/api/settings", `{"apiKey":"sk-ant-12345678","baseUrl":"","model":""}`, ckUser)
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-admin save settings = %d; want 403", w.Code)
	}
	w, _ = doJSON(t, s, "GET", "/api/settings", "", ckUser)
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-admin get settings = %d; want 403", w.Code)
	}
	// admin 保存 → key 落 secrets（settings 表无明文）；掩码回显
	w, _ = doJSON(t, s, "PUT", "/api/settings", `{"apiKey":"sk-ant-12345678","baseUrl":"","model":""}`, ck)
	if w.Code != http.StatusOK {
		t.Fatalf("save settings = %d", w.Code)
	}
	if v, _ := s.core.Store().GetSetting("api_key"); v != "" {
		t.Fatalf("api_key 不得再存 settings 明文: %q", v)
	}
	if v, err := s.core.Secrets().Get("api_key"); err != nil || v != "sk-ant-12345678" {
		t.Fatalf("api_key 应落 secrets: %q (%v)", v, err)
	}
	if s.cfg.APIKey != "sk-ant-12345678" {
		t.Fatalf("内存 cfg 未更新: len=%d", len(s.cfg.APIKey))
	}
	_, m := doJSON(t, s, "GET", "/api/settings", "", ck)
	if m["apiKey"] != "sk-****5678" {
		t.Fatalf("masked apiKey = %v", m["apiKey"])
	}
}

func TestLoginUnknownUser(t *testing.T) {
	s := newTestServer(t, map[string]string{"alice": "pw1"})
	w, m := doJSON(t, s, "POST", "/api/login", `{"username":"nobody","password":"x"}`, "")
	if w.Code != http.StatusUnauthorized || m["error"] != "用户名或密码错误" {
		t.Fatalf("unknown user = %d %v", w.Code, m)
	}
}

func TestHostsUnknownAPI404(t *testing.T) {
	// /api/* 未命中不得落进 SPA fallback（T1.4 审查 Issue 3 的回归线；
	// static 为 nil 时不挂 "/"，故此处断言 mux 的 404 而非 200 index.html）
	s := newTestServer(t, nil)
	w, _ := doJSON(t, s, "GET", "/api/nonexistent", "", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown api = %d; want 404", w.Code)
	}
}

func TestHostsNoResurrect(t *testing.T) {
	s := newTestServer(t, map[string]string{"alice": "pw1"})
	ck := login(t, s, "alice", "pw1")
	_, m := doJSON(t, s, "POST", "/api/hosts", `{"name":"web1","addr":"10.0.0.1","user":"root","authMethod":"password"}`, ck)
	id, _ := m["id"].(string)
	doJSON(t, s, "DELETE", "/api/hosts/"+id, "", ck)
	// 携带旧 id 再编辑 → 400 主机已被删除，不得复活
	edit := fmt.Sprintf(`{"id":%q,"name":"web1","addr":"10.0.0.1","user":"root","authMethod":"password"}`, id)
	w, me := doJSON(t, s, "POST", "/api/hosts", edit, ck)
	if w.Code != http.StatusBadRequest || !strings.Contains(fmt.Sprint(me["error"]), "已被删除") {
		t.Fatalf("resurrect = %d %v; want 400 已被删除", w.Code, me)
	}
	_, m = doJSON(t, s, "GET", "/api/hosts", "", ck)
	if n := len(m["hosts"].([]any)); n != 0 {
		t.Fatalf("host resurrected: %d", n)
	}
}

// TestSaveHostServerError 钥匙串故障是服务端问题 → 500（而非 400）；
// 不涉秘密的新建不受影响。
func TestSaveHostServerError(t *testing.T) {
	s := newTestServerAt(t, map[string]string{"alice": "pw1"}, failingSecrets{})
	ck := login(t, s, "alice", "pw1")
	w, m := doJSON(t, s, "POST", "/api/hosts", `{"name":"web1","addr":"10.0.0.1","user":"root","authMethod":"password","password":"x"}`, ck)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("secrets failure = %d %v; want 500", w.Code, m)
	}
	// keyPath 传 keysDir 内合法路径（failingSecrets 不涉 KeyPath 落盘路径，
	// 只是 SecretStore 写失败）：无 password/passphrase 的密钥认证主机
	// 保存不触钥匙串——但本例 no-secret 意在验证「不涉秘密可保存」，
	// 密码认证缺密码即无秘密写。改用 key + keysDir 内路径。
	w, _ = doJSON(t, s, "POST", "/api/hosts",
		fmt.Sprintf(`{"name":"web2","addr":"10.0.0.2","user":"root","authMethod":"key","keyPath":%q}`,
			filepath.Join(s.keysDir, "k1.key")), ck)
	if w.Code != http.StatusOK {
		t.Fatalf("no-secret create = %d; want 200", w.Code)
	}
}

// TestSaveHostKeyPathWhitelist KeyPath 白名单（评审 ①-5）：客户端声明的
// keyPath 只接受 <data>/keys 内的路径；任意路径（服务器本地私钥引用）
// 一律 400「请上传」。keysDir 内路径放行（存量主机编辑沿用场景）。
func TestSaveHostKeyPathWhitelist(t *testing.T) {
	s := newTestServer(t, map[string]string{"alice": "pw1"})
	ck := login(t, s, "alice", "pw1")
	// 越界路径 → 400
	for _, bad := range []string{
		"/root/.ssh/id_rsa",
		filepath.Join(s.keysDir, "..", "escape.key"), // ../ 逃逸
		"relative.key",                               // 相对路径
		filepath.Join(filepath.Dir(s.keysDir), "x.key"),
	} {
		w, m := doJSON(t, s, "POST", "/api/hosts",
			fmt.Sprintf(`{"name":"bad","addr":"10.0.0.1","user":"root","authMethod":"key","keyPath":%q}`, bad), ck)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("keyPath %q = %d %v; want 400", bad, w.Code, m)
		}
	}
	// keysDir 内 → 放行
	good := filepath.Join(s.keysDir, "abc.key")
	if err := os.MkdirAll(s.keysDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(good, []byte("placeholder"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, _ := doJSON(t, s, "POST", "/api/hosts",
		fmt.Sprintf(`{"name":"ok","addr":"10.0.0.1","user":"root","authMethod":"key","keyPath":%q}`, good), ck)
	if w.Code != http.StatusOK {
		t.Fatalf("keysDir 内 keyPath = %d; want 200", w.Code)
	}
}

// genKeyPEM 生成一把测试私钥 PEM；passphrase 非空时加密。
func genKeyPEM(t *testing.T, passphrase string) []byte {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	var block *pem.Block
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(priv, "")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	}
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return pem.EncodeToMemory(block)
}

func TestSaveHostKeyUpload(t *testing.T) {
	s := newTestServer(t, map[string]string{"alice": "pw1"})
	ck := login(t, s, "alice", "pw1")
	pem := genKeyPEM(t, "")
	body := fmt.Sprintf(`{"name":"k1","addr":"10.0.0.1","user":"root","authMethod":"key","keyData":"%s"}`,
		base64.StdEncoding.EncodeToString(pem))
	w, m := doJSON(t, s, "POST", "/api/hosts", body, ck)
	if w.Code != http.StatusOK {
		t.Fatalf("save = %d, %v", w.Code, m)
	}
	id := m["id"].(string)
	// 文件落盘：路径 = keysDir/<id>.key，内容逐字节一致
	path := filepath.Join(s.keysDir, id+".key")
	raw, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(raw, pem) {
		t.Fatalf("key file = %v, %v; want exact bytes", raw, err)
	}
	// 清单回显 KeyPath 指向该文件
	w, m = doJSON(t, s, "GET", "/api/hosts", "", ck)
	hs := m["hosts"].([]any)
	h0 := hs[0].(map[string]any)
	if h0["keyPath"] != path {
		t.Fatalf("keyPath = %v; want %s", h0["keyPath"], path)
	}
	// 编辑不重传：保留原 KeyPath；重传：文件内容被覆盖
	w, _ = doJSON(t, s, "POST", "/api/hosts", fmt.Sprintf(`{"id":%q,"name":"k1","addr":"10.0.0.1","user":"root","authMethod":"key"}`, id), ck)
	if w.Code != http.StatusOK {
		t.Fatalf("edit keep = %d", w.Code)
	}
	pem2 := genKeyPEM(t, "")
	w, _ = doJSON(t, s, "POST", "/api/hosts", fmt.Sprintf(`{"id":%q,"name":"k1","addr":"10.0.0.1","user":"root","authMethod":"key","keyData":"%s"}`,
		id, base64.StdEncoding.EncodeToString(pem2)), ck)
	if w.Code != http.StatusOK {
		t.Fatalf("edit replace = %d", w.Code)
	}
	if raw2, _ := os.ReadFile(path); !bytes.Equal(raw2, pem2) {
		t.Fatal("key file should be replaced")
	}
	// 删除主机 → key 文件顺带清理
	w, _ = doJSON(t, s, "DELETE", "/api/hosts/"+id, "", ck)
	if w.Code != http.StatusOK {
		t.Fatalf("delete = %d", w.Code)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("key file should be removed, stat err = %v", err)
	}
}

func TestSaveHostKeyValidation(t *testing.T) {
	s := newTestServer(t, map[string]string{"alice": "pw1"})
	ck := login(t, s, "alice", "pw1")
	// 坏私钥 → 400
	w, m := doJSON(t, s, "POST", "/api/hosts",
		`{"name":"bad","addr":"10.0.0.1","user":"root","authMethod":"key","keyData":"`+base64.StdEncoding.EncodeToString([]byte("not a key"))+`"}`, ck)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad key = %d, %v; want 400", w.Code, m)
	}
	// 新建密钥认证缺 keyData 与 keyPath → 400
	w, _ = doJSON(t, s, "POST", "/api/hosts", `{"name":"nop","addr":"10.0.0.1","user":"root","authMethod":"key"}`, ck)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing key = %d; want 400", w.Code)
	}
	// 加密私钥：错误口令 400、正确口令 200
	enc := genKeyPEM(t, "secret1")
	encB64 := base64.StdEncoding.EncodeToString(enc)
	w, _ = doJSON(t, s, "POST", "/api/hosts",
		`{"name":"enc","addr":"10.0.0.1","user":"root","authMethod":"key","keyData":"`+encB64+`","keyPassphrase":"wrong"}`, ck)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("wrong passphrase = %d; want 400", w.Code)
	}
	w, _ = doJSON(t, s, "POST", "/api/hosts",
		`{"name":"enc","addr":"10.0.0.1","user":"root","authMethod":"key","keyData":"`+encB64+`","keyPassphrase":"secret1"}`, ck)
	if w.Code != http.StatusOK {
		t.Fatalf("right passphrase = %d; want 200", w.Code)
	}
}

func TestHostDraft(t *testing.T) {
	s := newTestServer(t, map[string]string{"alice": "pw1"})
	ck := login(t, s, "alice", "pw1")
	// 缺地址/用户 → 400
	w, _ := doJSON(t, s, "POST", "/api/hosts/test", `{"name":"x","user":"root","authMethod":"key"}`, ck)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing addr = %d; want 400", w.Code)
	}
	// 拨不通（127.0.0.1:1 拒连）→ 502 连接失败；无 ID 不写侧栏状态
	w, m := doJSON(t, s, "POST", "/api/hosts/test",
		`{"name":"x","addr":"127.0.0.1","port":1,"user":"root","authMethod":"password","password":"p"}`, ck)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("unreachable = %d, %v; want 502", w.Code, m)
	}
	if s.getStatus("nope") != "unknown" {
		t.Fatal("draft test must not touch status without ID")
	}
}
