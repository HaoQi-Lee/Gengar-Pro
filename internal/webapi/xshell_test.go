package webapi

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"gengar-pro/internal/audit"
	"gengar-pro/internal/xshell"
)

func b64Files(fs ...xshell.File) string {
	out := "["
	for i, f := range fs {
		if i > 0 {
			out += ","
		}
		out += fmt.Sprintf(`{"name":%q,"data":%q}`, f.Name, base64.StdEncoding.EncodeToString(f.Content))
	}
	return out + "]"
}

// xshFor 就地构造 UTF-16LE BOM 的最小 .xsh 内容（webapi 无法复用
// xshell 包私有构造；与 xshell 包测试同思路）。
func xshFor(host, proto string) []byte {
	var b []byte = []byte{0xFF, 0xFE}
	text := "[CONNECTION]\nProtocol=" + proto + "\nHost=" + host + "\nPort=22\n" +
		"[CONNECTION:AUTHORIZATION]\nUserName=root\n"
	for _, r := range text {
		b = append(b, byte(r), byte(r>>8))
	}
	return b
}

func TestXshellPreviewAndCommit(t *testing.T) {
	s := newTestServer(t, map[string]string{"alice": "pw1"})
	ck := login(t, s, "alice", "pw1")

	ssh1 := xshFor("10.0.0.1", "SSH")
	telnet := xshFor("10.0.0.2", "TELNET")
	body := `{"files":` + b64Files(
		xshell.File{Name: "web/a.xsh", Content: ssh1},
		xshell.File{Name: "tel.xsh", Content: telnet},
	) + `}`
	w, m := doJSON(t, s, "POST", "/api/xshell/import", body, ck)
	if w.Code != 200 {
		t.Fatalf("preview = %d %v", w.Code, m)
	}
	rows, _ := m["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("rows = %d; want 1（telnet 跳过）", len(rows))
	}
	if m["skipped"].(float64) != 1 {
		t.Fatalf("skipped = %v", m["skipped"])
	}
	r0 := rows[0].(map[string]any)
	if r0["host"] != "10.0.0.1" || r0["folder"] != "web" {
		t.Fatalf("row = %v", r0)
	}

	// commit：勾选行 → 建主机
	commit := `{"hosts":[{"name":"a","addr":"10.0.0.1","port":22,"user":"root","tags":["web"],"folder":"web","authMethod":"password"}]}`
	w, _ = doJSON(t, s, "POST", "/api/xshell/commit", commit, ck)
	if w.Code != 200 {
		t.Fatalf("commit = %d", w.Code)
	}
	_, m = doJSON(t, s, "GET", "/api/hosts", "", ck)
	if n := len(m["hosts"].([]any)); n != 1 {
		t.Fatalf("hosts after commit = %d", n)
	}

	// 再次 preview：同 addr+port+user → duplicate 标记
	w, m = doJSON(t, s, "POST", "/api/xshell/import", `{"files":`+b64Files(xshell.File{Name: "a.xsh", Content: ssh1})+`}`, ck)
	if w.Code != 200 {
		t.Fatalf("re-preview = %d %v", w.Code, m)
	}
	rows = m["rows"].([]any)
	if rows[0].(map[string]any)["duplicate"] != true {
		t.Fatalf("duplicate not flagged: %v", rows[0])
	}
}

func TestXshellBadBase64(t *testing.T) {
	s := newTestServer(t, map[string]string{"alice": "pw1"})
	ck := login(t, s, "alice", "pw1")
	w, _ := doJSON(t, s, "POST", "/api/xshell/import", `{"files":[{"name":"a.xsh","data":"!!!"}]}`, ck)
	if w.Code != 400 {
		t.Fatalf("bad base64 = %d; want 400", w.Code)
	}
}

func TestXshellNonXshFiltered(t *testing.T) {
	s := newTestServer(t, map[string]string{"alice": "pw1"})
	ck := login(t, s, "alice", "pw1")
	// 服务端扩展名防御：非 .xsh 上传不计入解析（T4.1 审查落点）
	body := `{"files":[{"name":"readme.txt","data":` + `"` + base64.StdEncoding.EncodeToString(xshFor("10.0.0.1", "SSH")) + `"` + `}]}`
	w, m := doJSON(t, s, "POST", "/api/xshell/import", body, ck)
	if w.Code != 200 {
		t.Fatalf("preview = %d", w.Code)
	}
	rows, _ := m["rows"].([]any)
	if len(rows) != 0 {
		t.Fatalf("rows = %d; want 0（非 .xsh 过滤）", len(rows))
	}
}

func TestXshellRequireAuth(t *testing.T) {
	s := newTestServer(t, nil)
	w, _ := doJSON(t, s, "POST", "/api/xshell/import", `{"files":[]}`, "")
	if w.Code != 401 {
		t.Fatalf("preview without login = %d", w.Code)
	}
}

// TestXshellImportAudit 导入审计：preview 落 xshell_import_preview
// （parsed=N hosts）、commit 落 xshell_import_commit（imported=N hosts）；
// 两次成功请求之外无其他条目。
func TestXshellImportAudit(t *testing.T) {
	s := newTestServer(t, map[string]string{"alice": "pw1"})
	ck := login(t, s, "alice", "pw1")
	l := openTestAudit(t, s)

	body := `{"files":` + b64Files(xshell.File{Name: "web/a.xsh", Content: xshFor("10.0.0.1", "SSH")}) + `}`
	w, _ := doJSON(t, s, "POST", "/api/xshell/import", body, ck)
	if w.Code != 200 {
		t.Fatalf("preview = %d", w.Code)
	}
	commit := `{"hosts":[{"name":"a","addr":"10.0.0.1","port":22,"user":"root","folder":"web","authMethod":"password"}]}`
	w, _ = doJSON(t, s, "POST", "/api/xshell/commit", commit, ck)
	if w.Code != 200 {
		t.Fatalf("commit = %d", w.Code)
	}

	es := readAuditEntries(t, l)
	if len(es) != 2 {
		t.Fatalf("审计条数 = %d; want 2: %+v", len(es), es)
	}
	var pv, cm *audit.Entry
	for i := range es {
		switch es[i].Tool {
		case "xshell_import_preview":
			pv = &es[i]
		case "xshell_import_commit":
			cm = &es[i]
		}
	}
	if pv == nil || !strings.Contains(pv.Output, "parsed=1") {
		t.Fatalf("xshell_import_preview 条目 = %+v", pv)
	}
	if cm == nil || !strings.Contains(cm.Output, "imported=1") {
		t.Fatalf("xshell_import_commit 条目 = %+v", cm)
	}
	// 全成功同样带 names 清单（可溯源到具体主机名）
	if !strings.Contains(string(cm.Input), `"names":["a"]`) {
		t.Fatalf("xshell_import_commit Input = %s; want names 含 a", cm.Input)
	}
}

// TestXshellCommitPartialAudit 部分失败溯源（审查遗留 A1）：commit 首错
// 中止后，已入库主机必须有审计痕迹——条目 Output=imported=<k>/failed_at=
// <失败名>，Input.names 含已入库名单；首台即失败（k=0）无任何入库，
// 不落条目（响应已报错，与现状一致）。
func TestXshellCommitPartialAudit(t *testing.T) {
	s := newTestServer(t, map[string]string{"alice": "pw1"})
	ck := login(t, s, "alice", "pw1")
	l := openTestAudit(t, s)

	// 第二台私钥 base64 非法 → 第一台入库后 400 中止
	commit := `{"hosts":[` +
		`{"name":"ok1","addr":"10.0.0.1","port":22,"user":"root","authMethod":"password"},` +
		`{"name":"bad1","addr":"10.0.0.2","port":22,"user":"root","authMethod":"key","keyData":"!!!"}]}`
	w, m := doJSON(t, s, "POST", "/api/xshell/commit", commit, ck)
	if w.Code != 400 {
		t.Fatalf("partial commit = %d %v; want 400", w.Code, m)
	}
	es := readAuditEntries(t, l)
	if len(es) != 1 || es[0].Tool != "xshell_import_commit" {
		t.Fatalf("部分失败条目 = %+v; want 1 条 xshell_import_commit", es)
	}
	if es[0].Output != "imported=1/failed_at=bad1" {
		t.Fatalf("Output = %q; want imported=1/failed_at=bad1", es[0].Output)
	}
	if !strings.Contains(string(es[0].Input), `"names":["ok1"]`) {
		t.Fatalf("Input = %s; want names 含 ok1（已落库的必须有痕迹）", es[0].Input)
	}
	// 条目没撒谎：第一台确已入库
	_, m = doJSON(t, s, "GET", "/api/hosts", "", ck)
	if n := len(m["hosts"].([]any)); n != 1 {
		t.Fatalf("hosts = %d; want 1（第一台应已入库）", n)
	}

	// 首台即失败（k=0）：响应 400，不追加条目
	w, _ = doJSON(t, s, "POST", "/api/xshell/commit",
		`{"hosts":[{"name":"bad2","addr":"10.0.0.3","user":"root","authMethod":"key","keyData":"!!!"}]}`, ck)
	if w.Code != 400 {
		t.Fatalf("first-fail commit = %d; want 400", w.Code)
	}
	if es = readAuditEntries(t, l); len(es) != 1 {
		t.Fatalf("首台失败后条数 = %d; want 1（无入库不记）", len(es))
	}
}
