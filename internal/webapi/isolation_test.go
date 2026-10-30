// isolation_test.go 主机按用户隔离端到端：列表/写操作/终端/导入/scope
// 的视角隔离（设计 §7）。admin 全见可操作；普通用户只见自己的；他人
// 主机统一 404 防存在性探测。
package webapi

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// seedIsolatedUsers 建 alice/bob/admin 三用户的服务器（admin 角色与生产
// ensureAdmin 对齐）。
func seedIsolatedUsers(t *testing.T) (*Server, string, string, string) {
	t.Helper()
	s := newTestServer(t, map[string]string{"admin": "pw-admin", "alice": "pw1", "bob": "pw2"})
	if err := s.core.Store().SetUserRole("admin", "admin"); err != nil {
		t.Fatalf("SetUserRole: %v", err)
	}
	return s, login(t, s, "alice", "pw1"), login(t, s, "bob", "pw2"), login(t, s, "admin", "pw-admin")
}

func createHost(t *testing.T, s *Server, ck, name string) string {
	t.Helper()
	w, m := doJSON(t, s, "POST", "/api/hosts",
		fmt.Sprintf(`{"name":%q,"addr":"10.0.0.1","user":"root","authMethod":"password"}`, name), ck)
	if w.Code != http.StatusOK {
		t.Fatalf("create %s = %d %v", name, w.Code, m)
	}
	id, _ := m["id"].(string)
	if id == "" {
		t.Fatalf("create %s returned empty id", name)
	}
	return id
}

func listNames(t *testing.T, s *Server, ck string) map[string]bool {
	t.Helper()
	w, m := doJSON(t, s, "GET", "/api/hosts", "", ck)
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d %v", w.Code, m)
	}
	out := map[string]bool{}
	for _, x := range m["hosts"].([]any) {
		h := x.(map[string]any)
		out[h["name"].(string)] = true
	}
	return out
}

func TestHostsListIsolated(t *testing.T) {
	s, ckA, ckB, ckAdmin := seedIsolatedUsers(t)
	idA := createHost(t, s, ckA, "alice-web")
	idB := createHost(t, s, ckB, "bob-db")
	_ = idB

	if got := listNames(t, s, ckA); got["alice-web"] != true || got["bob-db"] != false {
		t.Fatalf("alice 视角 = %v", got)
	}
	if got := listNames(t, s, ckB); got["bob-db"] != true || got["alice-web"] != false {
		t.Fatalf("bob 视角 = %v", got)
	}
	if got := listNames(t, s, ckAdmin); got["alice-web"] != true || got["bob-db"] != true {
		t.Fatalf("admin 视角 = %v", got)
	}

	// bob 编辑 alice 主机 → 400「主机已被删除」（saveHost 既有文案，防探测）
	edit := fmt.Sprintf(`{"id":%q,"name":"alice-web","addr":"10.0.0.1","user":"root","authMethod":"password"}`, idA)
	w, m := doJSON(t, s, "POST", "/api/hosts", edit, ckB)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bob 编辑 alice 主机 = %d %v; want 400", w.Code, m)
	}
	// bob 删除 alice 主机 → 404
	w, _ = doJSON(t, s, "DELETE", "/api/hosts/"+idA, "", ckB)
	if w.Code != http.StatusNotFound {
		t.Fatalf("bob 删除 alice 主机 = %d; want 404", w.Code)
	}
	// bob 测试 alice 主机 → 404
	w, _ = doJSON(t, s, "POST", "/api/hosts/"+idA+"/test", "", ckB)
	if w.Code != http.StatusNotFound {
		t.Fatalf("bob 测 alice 主机 = %d; want 404", w.Code)
	}
	// bob 开 alice 主机终端 → 404
	w, _ = doJSON(t, s, "POST", "/api/hosts/"+idA+"/term", "", ckB)
	if w.Code != http.StatusNotFound {
		t.Fatalf("bob 开 alice 主机终端 = %d; want 404", w.Code)
	}
	// bob 草稿测试继承 alice 主机 → 404
	w, _ = doJSON(t, s, "POST", "/api/hosts/test", edit, ckB)
	if w.Code != http.StatusNotFound {
		t.Fatalf("bob 草稿测试 alice 主机 = %d; want 404", w.Code)
	}

	// alice 自己编辑照常
	edit2 := fmt.Sprintf(`{"id":%q,"name":"alice-web2","addr":"10.0.0.1","user":"root","authMethod":"password"}`, idA)
	w, _ = doJSON(t, s, "POST", "/api/hosts", edit2, ckA)
	if w.Code != http.StatusOK {
		t.Fatalf("alice 编辑自己主机 = %d; want 200", w.Code)
	}
	// admin 可编辑他人主机
	edit3 := fmt.Sprintf(`{"id":%q,"name":"alice-web3","addr":"10.0.0.1","user":"root","authMethod":"password"}`, idA)
	w, _ = doJSON(t, s, "POST", "/api/hosts", edit3, ckAdmin)
	if w.Code != http.StatusOK {
		t.Fatalf("admin 编辑 alice 主机 = %d; want 200", w.Code)
	}
}

func TestXshellDedupPerViewer(t *testing.T) {
	s, ckA, ckB, _ := seedIsolatedUsers(t)
	// alice 建一台 10.0.0.1:22/root（经导入路径建归属）
	w, _ := doJSON(t, s, "POST", "/api/xshell/commit",
		`{"hosts":[{"name":"a","addr":"10.0.0.1","port":22,"user":"root","authMethod":"password"}]}`, ckA)
	if w.Code != http.StatusOK {
		t.Fatalf("alice commit = %d", w.Code)
	}
	// bob 导入同 addr+port+user 的会话：bob 视角内无重复 → duplicate=false
	body := `{"files":[{"name":"a.xsh","data":"` + b64Xsh("10.0.0.1") + `"}]}`
	w, m := doJSON(t, s, "POST", "/api/xshell/import", body, ckB)
	if w.Code != http.StatusOK {
		t.Fatalf("bob preview = %d %v", w.Code, m)
	}
	rows := m["rows"].([]any)
	if rows[0].(map[string]any)["duplicate"] != false {
		t.Fatalf("bob 视角内不应判重: %v", rows[0])
	}
	// alice 自己再预览 → duplicate=true
	w, m = doJSON(t, s, "POST", "/api/xshell/import", body, ckA)
	if w.Code != http.StatusOK {
		t.Fatalf("alice preview = %d", w.Code)
	}
	rows = m["rows"].([]any)
	if rows[0].(map[string]any)["duplicate"] != true {
		t.Fatalf("alice 视角内应判重: %v", rows[0])
	}
}

// b64Xsh 构造 UTF-16LE BOM 的最小 .xsh（与 xshell_test.go xshFor 同法，
// 放这里供隔离用例复用）。
func b64Xsh(hostAddr string) string {
	var b []byte = []byte{0xFF, 0xFE}
	text := "[CONNECTION]\nProtocol=SSH\nHost=" + hostAddr + "\nPort=22\n" +
		"[CONNECTION:AUTHORIZATION]\nUserName=root\n"
	for _, r := range text {
		b = append(b, byte(r), byte(r>>8))
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestDeleteUserReassignsHosts(t *testing.T) {
	s, ckA, ckB, ckAdmin := seedIsolatedUsers(t)
	createHost(t, s, ckB, "bob-db")
	createHost(t, s, ckB, "bob-web")
	createHost(t, s, ckA, "alice-web")

	// 删除 bob → 主机移交 admin
	w, m := doJSON(t, s, "DELETE", "/api/users/bob", "", ckAdmin)
	if w.Code != http.StatusOK {
		t.Fatalf("delete bob = %d %v", w.Code, m)
	}
	// bob 的主机 admin 全见；alice 视角不受影响
	got := listNames(t, s, ckAdmin)
	if got["bob-db"] != true || got["bob-web"] != true {
		t.Fatalf("admin 视角缺 bob 主机: %v", got)
	}
	if got := listNames(t, s, ckA); got["alice-web"] != true || got["bob-db"] != false {
		t.Fatalf("alice 视角被波及: %v", got)
	}
}

// TestScopePerViewer bob 的会话目标不能钉住 alice 的主机（Agent 目标
// 解析隔离，设计 §4.2/§4.3）。
func TestScopePerViewer(t *testing.T) {
	s, ckA, _, _ := seedIsolatedUsers(t)
	idA := createHost(t, s, ckA, "alice-web")

	// bob 建 WS 会话（chatOpen 走 bob 视角）
	hostAddr := startHTTP(t, s)
	ckB := login(t, s, "bob", "pw2")
	_, _, h := wsChatOpen(t, hostAddr, ckB, 0, "")
	sid := h.Session

	// bob 尝试钉住 alice 的主机 ID → 过滤为空
	w, _ := doJSON(t, s, "PUT", "/api/chat/scope",
		fmt.Sprintf(`{"session":%q,"hostIds":[%q]}`, sid, idA), ckB)
	if w.Code != http.StatusOK {
		t.Fatalf("set scope = %d", w.Code)
	}
	cs := s.chatFor(sid)
	if names := s.scopeNames(cs); names != "" {
		t.Fatalf("bob 钉住 alice 主机后 scopeNames = %q; want 空", names)
	}

	// bob 的 CatalogText 不含 alice 主机（Agent 系统提示词隔离）
	if cat := cs.sess.CatalogText(); strings.Contains(cat, "alice-web") {
		t.Fatalf("bob catalog 含 alice 主机:\n%s", cat)
	}
}
