package host

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func TestStoreSaveAndList(t *testing.T) {
	s := openTestStore(t)
	h := &Host{ID: "h1", Name: "web-01", Addr: "10.0.0.1", Port: 22, User: "root",
		Auth: AuthConfig{Method: AuthKey, KeyPath: "~/.ssh/id_ed25519"}, Tags: []string{"web"}}
	if err := s.Save(h); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := s.List()
	if err != nil || len(got) != 1 || got[0].Name != "web-01" || len(got[0].Tags) != 1 {
		t.Fatalf("list: %v %v", got, err)
	}
	if got[0].Auth.Method != AuthKey || got[0].Auth.KeyPath != "~/.ssh/id_ed25519" {
		t.Fatalf("auth roundtrip: %+v", got[0].Auth)
	}
}

func TestStoreUpsertByID(t *testing.T) {
	s := openTestStore(t)
	_ = s.Save(&Host{ID: "h1", Name: "web-01", Addr: "10.0.0.1", Port: 22, User: "root"})
	_ = s.Save(&Host{ID: "h1", Name: "web-01b", Addr: "10.0.0.1", Port: 22, User: "root"})
	got, _ := s.List()
	if len(got) != 1 || got[0].Name != "web-01b" {
		t.Fatalf("upsert should replace by ID, got %v", got)
	}
}

func TestStoreDelete(t *testing.T) {
	s := openTestStore(t)
	_ = s.Save(&Host{ID: "h1", Name: "web-01", Addr: "10.0.0.1", Port: 22, User: "root"})
	if err := s.Delete("h1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, _ := s.List()
	if len(got) != 0 {
		t.Fatalf("expected empty after delete, got %v", got)
	}
}

func TestStoreSettingsKV(t *testing.T) {
	s := openTestStore(t)
	if err := s.SetSetting("model", "claude-sonnet-4-20250514"); err != nil {
		t.Fatalf("set: %v", err)
	}
	v, err := s.GetSetting("model")
	if err != nil || v != "claude-sonnet-4-20250514" {
		t.Fatalf("get: %q %v", v, err)
	}
	v2, err := s.GetSetting("missing")
	if err != nil || v2 != "" {
		t.Fatalf("missing key should be empty, got %q %v", v2, err)
	}
}

func TestStoreReopenPersists(t *testing.T) {
	path := t.TempDir() + "/test.db"
	s, err := OpenStore(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	h := &Host{ID: "h1", Name: "web-01", Addr: "10.0.0.1", Port: 22, User: "root",
		Auth: AuthConfig{Method: AuthKey, KeyPath: "~/.ssh/id_ed25519"}, Tags: []string{"web"}}
	if err := s.Save(h); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := s.SetSetting("model", "m1"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	s2, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	got, err := s2.List()
	if err != nil || len(got) != 1 || got[0].Name != "web-01" || got[0].Auth.KeyPath != "~/.ssh/id_ed25519" {
		t.Fatalf("list after reopen: %v %v", got, err)
	}
	if v, err := s2.GetSetting("model"); err != nil || v != "m1" {
		t.Fatalf("setting after reopen: %q %v", v, err)
	}
}

// TestFolderMigrationAndRoundTrip 旧库（无 folder 列）打开即迁移、幂等、
// 读写往返规范化（设计 §3.1：CREATE TABLE IF NOT EXISTS 不改旧库）。
func TestFolderMigrationAndRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	// 模拟旧库：手工建无 folder 列的表并写入一行
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`CREATE TABLE hosts (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE,
		addr TEXT NOT NULL, port INTEGER NOT NULL, user TEXT NOT NULL, auth_json TEXT NOT NULL,
		tags_json TEXT NOT NULL, os TEXT NOT NULL DEFAULT '', role TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`INSERT INTO hosts (id, name, addr, port, user, auth_json, tags_json, os, role)
		VALUES ('a1', 'old-host', '10.0.0.1', 22, 'root', '{}', '[]', '', '')`); err != nil {
		t.Fatal(err)
	}
	_ = old.Close()

	s, err := OpenStore(path) // 打开即迁移
	if err != nil {
		t.Fatalf("open with migration: %v", err)
	}
	defer s.Close()
	hs, err := s.List()
	if err != nil || len(hs) != 1 || hs[0].Folder != "" {
		t.Fatalf("old host folder = %+v err=%v（迁移后应为根目录）", hs, err)
	}
	// 幂等：再次打开不报 duplicate column
	if s2, err := OpenStore(path); err != nil {
		t.Fatalf("reopen: %v", err)
	} else {
		_ = s2.Close()
	}
	// 读写往返（Save 内规范化）
	h := hs[0]
	h.Folder = " 生产/web/ "
	if err := s.Save(h); err != nil {
		t.Fatal(err)
	}
	got, _ := s.List()
	if got[0].Folder != "生产/web" {
		t.Fatalf("round-trip folder = %q, want 生产/web", got[0].Folder)
	}
}

// TestStoreConcurrentWrites 复现审查发现的 "database is locked"：
// modernc.org/sqlite 无默认 busy_timeout，database/sql 默认无界连接池，
// 并发写必须被串行化（SetMaxOpenConns(1)），否则高概率失败。
func TestStoreConcurrentWrites(t *testing.T) {
	s := openTestStore(t)
	const goroutines, perG = 8, 50
	var wg sync.WaitGroup
	errs := make(chan error, goroutines*perG*2)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				h := &Host{ID: fmt.Sprintf("h-%d-%d", g, i), Name: fmt.Sprintf("web-%d-%d", g, i),
					Addr: "10.0.0.1", Port: 22, User: "root"}
				if err := s.Save(h); err != nil {
					errs <- fmt.Errorf("save %s: %w", h.ID, err)
					continue
				}
				if err := s.SetSetting(fmt.Sprintf("k-%d-%d", g, i), "v"); err != nil {
					errs <- fmt.Errorf("set setting k-%d-%d: %w", g, i, err)
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	got, err := s.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != goroutines*perG {
		t.Fatalf("expected %d hosts, got %d", goroutines*perG, len(got))
	}
}

// TestOwnerMigrationAndListFor 旧库（无 owner 列）打开即迁移；ListFor 按
// 视角过滤（空 owner 归一 admin）；ReassignHosts 划转往返（设计 §3）。
func TestOwnerMigrationAndListFor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	// 模拟旧库：无 owner 列的 hosts 表 + 一行存量数据
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`CREATE TABLE hosts (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE,
		addr TEXT NOT NULL, port INTEGER NOT NULL, user TEXT NOT NULL, auth_json TEXT NOT NULL,
		tags_json TEXT NOT NULL, os TEXT NOT NULL DEFAULT '', folder TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`INSERT INTO hosts (id, name, addr, port, user, auth_json, tags_json, os, folder)
		VALUES ('a1', 'old-host', '10.0.0.1', 22, 'root', '{}', '[]', '', '')`); err != nil {
		t.Fatal(err)
	}
	_ = old.Close()

	s, err := OpenStore(path) // 打开即迁移
	if err != nil {
		t.Fatalf("open with migration: %v", err)
	}
	defer s.Close()
	// 幂等：再次打开不报 duplicate column
	if s2, err := OpenStore(path); err != nil {
		t.Fatalf("reopen: %v", err)
	} else {
		_ = s2.Close()
	}

	// 存量行 owner=""（DB 直读验证迁移补列默认值）
	var owner string
	if err := s.db.QueryRow(`SELECT owner FROM hosts WHERE id = 'a1'`).Scan(&owner); err != nil || owner != "" {
		t.Fatalf("old host owner = %q, err=%v; want ''", owner, err)
	}

	// 新建两台归属不同用户的主机
	save := func(id, name, owner string) {
		t.Helper()
		if err := s.Save(&Host{ID: id, Name: name, Addr: "10.0.0.1", Port: 22, User: "root", Owner: owner}); err != nil {
			t.Fatalf("save %s: %v", id, err)
		}
	}
	save("b1", "alice-host", "alice")
	save("c1", "bob-host", "bob")

	// alice 视角：只见自己的（存量 old-host 空 owner 归 admin，不可见）
	got, err := s.ListFor("alice", false)
	if err != nil {
		t.Fatalf("ListFor(alice): %v", err)
	}
	if len(got) != 1 || got[0].Name != "alice-host" {
		t.Fatalf("ListFor(alice) = %v; want [alice-host]", got)
	}
	// bob 视角
	got, _ = s.ListFor("bob", false)
	if len(got) != 1 || got[0].Name != "bob-host" {
		t.Fatalf("ListFor(bob) = %v; want [bob-host]", got)
	}
	// admin 视角：全表 3 行（含空 owner 存量行——归一语义）
	got, _ = s.ListFor("admin", true)
	if len(got) != 3 {
		t.Fatalf("ListFor(admin,true) = %v; want 3 行", got)
	}
	// List() 仍是全表（Core/headless 全局语义不变）
	got, _ = s.List()
	if len(got) != 3 {
		t.Fatalf("List() = %v; want 3 行", got)
	}

	// ReassignHosts：bob 的主机划给 admin；bob 视角变空、admin 视角含它
	if err := s.ReassignHosts("bob", "admin"); err != nil {
		t.Fatalf("ReassignHosts: %v", err)
	}
	got, _ = s.ListFor("bob", false)
	if len(got) != 0 {
		t.Fatalf("ListFor(bob) after reassign = %v; want empty", got)
	}
	got, _ = s.ListFor("admin", true)
	if len(got) != 3 {
		t.Fatalf("ListFor(admin,true) after reassign = %v; want 3 行", got)
	}
	// Save 往返：owner 字段不丢
	for _, h := range got {
		if h.Name == "bob-host" && h.Owner != "admin" {
			t.Fatalf("bob-host owner = %q; want admin", h.Owner)
		}
	}
}
