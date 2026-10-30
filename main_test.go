package main

import (
	"path/filepath"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"gengar-pro/internal/host"
)

func openTestStore(t *testing.T) *host.Store {
	t.Helper()
	s, err := host.OpenStore(filepath.Join(t.TempDir(), "gengar.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// 空库 → 创建 admin/admin（role=admin）；重复执行幂等不重置密码。
func TestEnsureAdminCreatesDefault(t *testing.T) {
	s := openTestStore(t)
	if err := ensureAdmin(s); err != nil {
		t.Fatalf("ensureAdmin: %v", err)
	}
	u, err := s.GetUser("admin")
	if err != nil || u.Role != "admin" {
		t.Fatalf("admin = %+v, %v; want Role admin", u, err)
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PwHash), []byte("admin")) != nil {
		t.Fatal("默认密码应为 admin")
	}
	if err := ensureAdmin(s); err != nil {
		t.Fatalf("ensureAdmin again: %v", err)
	}
}

// 旧库 CLI 建的 admin（无角色）→ 补提权，密码不动。
func TestEnsureAdminUpgradesLegacyRole(t *testing.T) {
	s := openTestStore(t)
	h, _ := bcrypt.GenerateFromPassword([]byte("changed"), bcrypt.MinCost)
	if err := s.CreateUser(host.User{Username: "admin", PwHash: string(h), CreatedAt: "t"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := ensureAdmin(s); err != nil {
		t.Fatalf("ensureAdmin: %v", err)
	}
	u, err := s.GetUser("admin")
	if err != nil || u.Role != "admin" {
		t.Fatalf("admin = %+v, %v; want Role admin", u, err)
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PwHash), []byte("changed")) != nil {
		t.Fatal("密码应保持原值（ensureAdmin 只补角色不动密码）")
	}
}
