package host

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func newTestEnc(t *testing.T) *EncryptedStore {
	t.Helper()
	e, err := OpenEncrypted(openTestStore(t), []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("OpenEncrypted: %v", err)
	}
	return e
}

func TestEncryptedRoundtrip(t *testing.T) {
	e := newTestEnc(t)
	if err := e.Set("ref1", "s3cret"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	v, err := e.Get("ref1")
	if err != nil || v != "s3cret" {
		t.Fatalf("Get = %q, %v", v, err)
	}
	// 覆盖写
	if err := e.Set("ref1", "new"); err != nil {
		t.Fatalf("Set overwrite: %v", err)
	}
	if v, _ := e.Get("ref1"); v != "new" {
		t.Fatalf("Get after overwrite = %q", v)
	}
	if err := e.Delete("ref1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := e.Get("ref1"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("Get after delete err = %v; want ErrSecretNotFound", err)
	}
}

func TestEncryptedCiphertextNotPlaintext(t *testing.T) {
	s := openTestStore(t)
	e, err := OpenEncrypted(s, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("OpenEncrypted: %v", err)
	}
	_ = e.Set("ref1", "s3cret")
	ct, _ := s.GetSecretBlob("ref1")
	if len(ct) < 29 { // 12 nonce + 16 tag + ≥1 密文
		t.Fatalf("ciphertext too short: %d bytes", len(ct))
	}
	if string(ct) == "s3cret" {
		t.Fatal("ciphertext equals plaintext")
	}
}

func TestEncryptedWrongKeyFails(t *testing.T) {
	s := openTestStore(t)
	e1, _ := OpenEncrypted(s, []byte("0123456789abcdef0123456789abcdef"))
	_ = e1.Set("ref1", "s3cret")
	// 同一 DB、不同主密钥 → 解密失败
	e2, err := OpenEncrypted(s, []byte("fedcba9876543210fedcba9876543210"))
	if err != nil {
		t.Fatalf("OpenEncrypted: %v", err)
	}
	if _, err := e2.Get("ref1"); err == nil {
		t.Fatal("wrong master key should fail to decrypt")
	}
}

func TestEncryptedTruncatedBlobFails(t *testing.T) {
	s := openTestStore(t)
	e, _ := OpenEncrypted(s, []byte("0123456789abcdef0123456789abcdef"))
	_ = e.Set("ref1", "s3cret")
	_ = s.SetSecretBlob("ref1", []byte{1, 2, 3}) // 直接写坏：截断密文
	if _, err := e.Get("ref1"); err == nil {
		t.Fatal("truncated blob should fail, not panic or return garbage")
	}
}

func TestOpenEncryptedKeyLength(t *testing.T) {
	if _, err := OpenEncrypted(openTestStore(t), []byte("0123456789abcdef0123456789abcde")); err == nil {
		t.Fatal("31-byte master key should fail")
	}
}

// TestEncryptedAADBindsRef 密文行错位重放防护：ref 的密文以 AAD 绑定进
// 认证标签，整行复制到别的 ref 后解不开。
func TestEncryptedAADBindsRef(t *testing.T) {
	s := openTestStore(t)
	e, _ := OpenEncrypted(s, []byte("0123456789abcdef0123456789abcdef"))
	_ = e.Set("ref1", "s3cret")
	ct, _ := s.GetSecretBlob("ref1")
	_ = s.SetSecretBlob("ref2", ct) // 错位重放
	if _, err := e.Get("ref2"); err == nil {
		t.Fatal("ciphertext copied to another ref should fail (AAD binding)")
	}
	// 原行不受影响
	if v, err := e.Get("ref1"); err != nil || v != "s3cret" {
		t.Fatalf("Get(ref1) after copy = %q, %v", v, err)
	}
}

func TestMasterKeyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.key")
	k1, err := LoadOrCreateMasterKey(path, "")
	if err != nil {
		t.Fatalf("LoadOrCreateMasterKey: %v", err)
	}
	if len(k1) != 32 {
		t.Fatalf("key len = %d; want 32", len(k1))
	}
	// 再读得到同一把钥匙
	k2, _ := LoadOrCreateMasterKey(path, "")
	if string(k1) != string(k2) {
		t.Fatal("reread should return same key")
	}
	// 环境变量优先（hex 32 字节）
	env := hex.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	k3, err := LoadOrCreateMasterKey(path, env)
	if err != nil || string(k3) != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("env key = %v, %v", k3, err)
	}
	// env 非 hex / 长度错 → 报错
	if _, err := LoadOrCreateMasterKey(path, "zz"); err == nil {
		t.Fatal("invalid env key should fail")
	}
	// env 合法 hex 但长度错（16 字节）→ 报错
	if _, err := LoadOrCreateMasterKey(path, hex.EncodeToString([]byte("0123456789abcdef"))); err == nil {
		t.Fatal("short env key should fail")
	}
	// 文件权限 0600（Windows 无 POSIX 权限位，Stat 恒报 0666，跳过）
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(path); err == nil && fi.Mode().Perm()&0o077 != 0 {
			t.Fatalf("secret.key perms = %v; want 0600", fi.Mode().Perm())
		}
	}
	// keyfile 存在但长度错（10 字节）→ 报错，绝不静默重生成
	shortPath := filepath.Join(dir, "short.key")
	if err := os.WriteFile(shortPath, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateMasterKey(shortPath, ""); err == nil {
		t.Fatal("short keyfile should fail")
	}
	// keyfile 路径不可读（此处以目录模拟：读错误≠不存在）→ 必须在读阶段
	// 报错退出，不能误当"不存在"重生成新钥匙覆盖（否则既有密文永久报废）
	dirPath := filepath.Join(dir, "occupied")
	if err := os.Mkdir(dirPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateMasterKey(dirPath, ""); err == nil || !strings.Contains(err.Error(), "read master key") {
		t.Fatalf("unreadable keyfile err = %v; want read-stage failure", err)
	}
}
