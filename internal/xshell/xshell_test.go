package xshell

import (
	"os"
	"path/filepath"
	"testing"
	"unicode/utf16"
)

// xshBytes 构造 UTF-16LE（BOM）编码的 .xsh 内容——XShell 8 实测格式。
func xshBytes(content string) []byte {
	b := []byte{0xFF, 0xFE}
	for _, u := range utf16.Encode([]rune(content)) {
		b = append(b, byte(u), byte(u>>8))
	}
	return b
}

// writeXSH 写一个 UTF-16LE（BOM）编码的 .xsh 测试文件。
func writeXSH(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, xshBytes(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// body 构造一份 .xsh 内容（INI；auth 块按需拼接）。
func body(conn, auth string) string {
	return "[CONNECTION]\r\n" + conn + "\r\n[CONNECTION:AUTHORIZATION]\r\n" + auth + "\r\n"
}

const normalAuth = "Method=0\r\nUserKey=\r\nUserName=ubuntu"

func TestScanNormal(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "minio"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeXSH(t, filepath.Join(dir, "web-01.xsh"),
		body("Port=22222\r\nHost=10.1.1.1\r\nProtocol=SSH", normalAuth))
	writeXSH(t, filepath.Join(dir, "minio", "s3.xsh"),
		body("Port=22\r\nHost=10.1.1.2\r\nProtocol=SSH", "UserName=root\r\nUserKey="))

	res, err := Scan(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if res.Skipped != 0 || res.Failed != 0 {
		t.Fatalf("skipped=%d failed=%d, want 0/0", res.Skipped, res.Failed)
	}
	if len(res.Sessions) != 2 {
		t.Fatalf("sessions=%d, want 2", len(res.Sessions))
	}
	// Scan 按 Source 排序：minio/s3.xsh < web-01.xsh
	s3 := res.Sessions[0]
	if s3.Name != "s3" || s3.User != "root" || len(s3.Tags) != 1 || s3.Tags[0] != "minio" {
		t.Fatalf("s3 = %+v", s3)
	}
	if s3.Source != "minio/s3.xsh" {
		t.Fatalf("s3 source = %q", s3.Source)
	}
	web := res.Sessions[1]
	if web.Name != "web-01" || web.Host != "10.1.1.1" || web.Port != 22222 || web.User != "ubuntu" {
		t.Fatalf("web-01 = %+v", web)
	}
	if web.AuthMethod != "password" || web.KeyPath != "" || len(web.Tags) != 0 {
		t.Fatalf("web-01 auth = %+v", web)
	}
}

func TestScanDefaultsAndSkips(t *testing.T) {
	dir := t.TempDir()
	// 端口缺省 22；UserName 空 → root（与 HostForm 默认一致）
	writeXSH(t, filepath.Join(dir, "nop.xsh"),
		body("Host=10.1.1.3\r\nProtocol=SSH", "UserName=\r\nUserKey="))
	// 非 SSH 协议 → Skipped
	writeXSH(t, filepath.Join(dir, "tel.xsh"),
		body("Host=10.1.1.4\r\nPort=23\r\nProtocol=TELNET", normalAuth))
	// Host 空 → Skipped
	writeXSH(t, filepath.Join(dir, "nohost.xsh"),
		body("Port=22\r\nProtocol=SSH", normalAuth))
	// 坏文件（非文本）→ Failed，整体继续
	os.WriteFile(filepath.Join(dir, "bad.xsh"), []byte{0xFF, 0xFE, 0xD8, 0x00, 0x01}, 0o644)

	res, err := Scan(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if res.Skipped != 2 || res.Failed != 1 {
		t.Fatalf("skipped=%d failed=%d, want 2/1", res.Skipped, res.Failed)
	}
	if len(res.Sessions) != 1 {
		t.Fatalf("sessions=%d, want 1", len(res.Sessions))
	}
	if s := res.Sessions[0]; s.Port != 22 || s.User != "root" {
		t.Fatalf("nop = %+v", s)
	}
}

func TestScanUTF8NoBOM(t *testing.T) {
	dir := t.TempDir()
	// 个别工具生成的文件无 BOM——按 UTF-8 容错读取
	if err := os.WriteFile(filepath.Join(dir, "u8.xsh"),
		[]byte(body("Host=10.1.1.5\r\nProtocol=SSH", "UserName=op\r\nUserKey=")), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Scan(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(res.Sessions) != 1 || res.Sessions[0].Host != "10.1.1.5" {
		t.Fatalf("utf8 result = %+v", res)
	}
}

func TestScanKeyAuth(t *testing.T) {
	dir := t.TempDir()
	writeXSH(t, filepath.Join(dir, "k.xsh"),
		body("Host=10.1.1.6\r\nProtocol=SSH", "UserName=op\r\nUserKey=C:\\Users\\me\\id_rsa"))
	res, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := res.Sessions[0]
	// 评审 ①-5：UserKey 标记 key 认证但路径不带入（服务器侧不存在该
	// 文件，且构成 KeyPath 越权引用面）——导入后需上传私钥。
	if s.AuthMethod != "key" || s.KeyPath != "" {
		t.Fatalf("key session = %+v; want auth=key keyPath=\"\"", s)
	}
}

func TestScanUTF8BOM(t *testing.T) {
	dir := t.TempDir()
	content := body("Host=10.1.1.7\r\nProtocol=SSH", "UserName=op\r\nUserKey=")
	if err := os.WriteFile(filepath.Join(dir, "bom.xsh"),
		append([]byte{0xEF, 0xBB, 0xBF}, content...), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Scan(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(res.Sessions) != 1 || res.Sessions[0].Host != "10.1.1.7" || res.Sessions[0].User != "op" {
		t.Fatalf("utf8-bom result = %+v", res)
	}
}

func TestScanValueWithEquals(t *testing.T) {
	dir := t.TempDir()
	writeXSH(t, filepath.Join(dir, "eq.xsh"),
		body("Host=10.1.1.8=x\r\nProtocol=SSH", "UserName=op\r\nUserKey="))
	res, err := Scan(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(res.Sessions) != 1 || res.Sessions[0].Host != "10.1.1.8=x" {
		t.Fatalf("value-with-equals = %+v", res)
	}
}

// 裸 ".xsh"（空名会话）解析出的 Name 为空，SaveHost 必拒且会中断整批
// 导入——源头即跳过，而非留到入库阶段失败。
func TestScanDotXshName(t *testing.T) {
	dir := t.TempDir()
	writeXSH(t, filepath.Join(dir, ".xsh"),
		body("Host=10.1.1.9\r\nProtocol=SSH", "UserName=op\r\nUserKey="))
	res, err := Scan(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(res.Sessions) != 0 || res.Skipped != 1 || res.Failed != 0 {
		t.Fatalf("dot-xsh result = %+v", res)
	}
}

// 端口超出 16 位范围属 INI 值损坏——回落缺省 22 而非带病入库。
func TestScanPortOutOfRange(t *testing.T) {
	dir := t.TempDir()
	writeXSH(t, filepath.Join(dir, "p.xsh"),
		body("Host=10.1.2.1\r\nPort=99999\r\nProtocol=SSH", "UserName=op\r\nUserKey="))
	res, err := Scan(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(res.Sessions) != 1 || res.Sessions[0].Port != 22 {
		t.Fatalf("port result = %+v", res)
	}
}

func TestScanMissingDir(t *testing.T) {
	if _, err := Scan(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("目录不存在应报错")
	}
}

func TestUniqueName(t *testing.T) {
	used := map[string]bool{"web": true, "web-2": true}
	if got := UniqueName("web", used); got != "web-3" {
		t.Fatalf("UniqueName(web) = %q, want web-3", got)
	}
	if got := UniqueName("db", used); got != "db" {
		t.Fatalf("UniqueName(db) = %q, want db", got)
	}
}

func TestParseFiles(t *testing.T) {
	res, err := ParseFiles([]File{
		{Name: "web/a.xsh", Content: xshBytes(body("Protocol=SSH\r\nHost=10.0.0.1\r\nPort=22", "UserName=root\r\nUserKey="))},
		{Name: "telnet.xsh", Content: xshBytes(body("Protocol=TELNET\r\nHost=10.0.0.2\r\nPort=23", normalAuth))},
		{Name: "web/db.xsh", Content: xshBytes(body("Protocol=SSH\r\nHost=10.0.0.3\r\nPort=22", normalAuth))},
	})
	if err != nil {
		t.Fatalf("ParseFiles: %v", err)
	}
	if len(res.Sessions) != 2 || res.Skipped != 1 || res.Failed != 0 {
		t.Fatalf("sessions=%d skipped=%d failed=%d", len(res.Sessions), res.Skipped, res.Failed)
	}
	var a, db Session
	for _, s := range res.Sessions {
		if s.Name == "a" {
			a = s
		}
		if s.Name == "db" {
			db = s
		}
	}
	if a.Source != "web/a.xsh" || len(a.Tags) != 1 || a.Tags[0] != "web" {
		t.Fatalf("a = %+v", a)
	}
	if db.Source != "web/db.xsh" || db.Tags[0] != "web" {
		t.Fatalf("db = %+v", db)
	}
}
