package host

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "gengar.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestUsersRoundtrip(t *testing.T) {
	s := openTestStore(t)
	if n, err := s.CountUsers(); err != nil || n != 0 {
		t.Fatalf("CountUsers = %d, %v; want 0, nil", n, err)
	}
	if err := s.CreateUser(User{Username: "alice", PwHash: "h1", CreatedAt: "2026-10-22"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	// 重名用户必须报错（不静默覆盖）
	if err := s.CreateUser(User{Username: "alice", PwHash: "h2", CreatedAt: "x"}); err == nil {
		t.Fatal("duplicate CreateUser should fail")
	}
	u, err := s.GetUser("alice")
	if err != nil || u.PwHash != "h1" {
		t.Fatalf("GetUser = %+v, %v; want PwHash h1", u, err)
	}
	if _, err := s.GetUser("bob"); !errors.Is(err, ErrNoSuchUser) {
		t.Fatalf("GetUser(bob) err = %v; want ErrNoSuchUser", err)
	}
	if n, _ := s.CountUsers(); n != 1 {
		t.Fatalf("CountUsers = %d; want 1", n)
	}
}

func TestUserManagementMethods(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateUser(User{Username: "bob", PwHash: "h1", CreatedAt: "t1"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := s.CreateUser(User{Username: "alice", PwHash: "h2", Role: "admin", CreatedAt: "t2"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	users, err := s.ListUsers()
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 2 || users[0].Username != "alice" || users[1].Username != "bob" {
		t.Fatalf("ListUsers = %+v; want alice,bob sorted", users)
	}
	if users[0].Role != "admin" || users[1].Role != "" {
		t.Fatalf("roles = %q,%q; want admin,''", users[0].Role, users[1].Role)
	}
	if err := s.UpdateUserPassword("bob", "h9"); err != nil {
		t.Fatalf("UpdateUserPassword: %v", err)
	}
	if u, err := s.GetUser("bob"); err != nil || u.PwHash != "h9" {
		t.Fatalf("bob after update = %+v, %v; want PwHash h9", u, err)
	}
	if err := s.SetUserRole("bob", "admin"); err != nil {
		t.Fatalf("SetUserRole: %v", err)
	}
	if _, err := s.GetUser("ghost"); !errors.Is(err, ErrNoSuchUser) {
		t.Fatalf("GetUser(ghost) = %v; want ErrNoSuchUser", err)
	}
	if err := s.UpdateUserPassword("ghost", "x"); !errors.Is(err, ErrNoSuchUser) {
		t.Fatalf("UpdateUserPassword(ghost) = %v; want ErrNoSuchUser", err)
	}
	if err := s.SetUserRole("ghost", "admin"); !errors.Is(err, ErrNoSuchUser) {
		t.Fatalf("SetUserRole(ghost) = %v; want ErrNoSuchUser", err)
	}
	if err := s.DeleteUser("bob"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, err := s.GetUser("bob"); !errors.Is(err, ErrNoSuchUser) {
		t.Fatalf("GetUser(bob) after delete = %v; want ErrNoSuchUser", err)
	}
}

func TestUsersRoleMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gengar.db")
	// 旧库：无 role 列的 users 表（模拟上一版库文件）
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open old: %v", err)
	}
	if _, err := old.Exec(`CREATE TABLE users (username TEXT PRIMARY KEY, pw_hash TEXT NOT NULL, created_at TEXT NOT NULL);
		INSERT INTO users (username, pw_hash, created_at) VALUES ('legacy', 'h', '2026-01-01')`); err != nil {
		t.Fatalf("seed old: %v", err)
	}
	_ = old.Close()
	// OpenStore 补列迁移后：旧数据可读、role 默认空、可设置
	s, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	u, err := s.GetUser("legacy")
	if err != nil || u.Role != "" {
		t.Fatalf("legacy user = %+v, %v; want Role ''", u, err)
	}
	if err := s.SetUserRole("legacy", "admin"); err != nil {
		t.Fatalf("SetUserRole: %v", err)
	}
	if u, _ = s.GetUser("legacy"); u.Role != "admin" {
		t.Fatalf("role after set = %q; want admin", u.Role)
	}
}

func TestSecretBlobs(t *testing.T) {
	s := openTestStore(t)
	if v, err := s.GetSecretBlob("r1"); err != nil || v != nil {
		t.Fatalf("GetSecretBlob missing = %v, %v; want nil, nil", v, err)
	}
	if err := s.SetSecretBlob("r1", []byte{1, 2, 3}); err != nil {
		t.Fatalf("SetSecretBlob: %v", err)
	}
	v, err := s.GetSecretBlob("r1")
	if err != nil || string(v) != "\x01\x02\x03" {
		t.Fatalf("GetSecretBlob = %v, %v", v, err)
	}
	if err := s.DeleteSecretBlob("r1"); err != nil {
		t.Fatalf("DeleteSecretBlob: %v", err)
	}
	// 幂等删除
	if err := s.DeleteSecretBlob("r1"); err != nil {
		t.Fatalf("DeleteSecretBlob again: %v", err)
	}
}
