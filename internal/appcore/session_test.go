package appcore

import (
	"strings"
	"testing"
	"time"

	"gengar-pro/internal/host"
)

func openTestCore(t *testing.T) *Core {
	t.Helper()
	c, err := OpenAt(t.TempDir(), host.MemoryStore{}, time.Second)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func seedHosts(t *testing.T, c *Core, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := c.Store().Save(&host.Host{ID: "id-" + n, Name: n, Addr: "10.0.0.1", Port: 22, User: "root"}); err != nil {
			t.Fatalf("seed %s: %v", n, err)
		}
	}
}

func TestSessionPinnedIsolation(t *testing.T) {
	c := openTestCore(t)
	seedHosts(t, c, "web", "db")
	sa, sb := c.NewSession(), c.NewSession()
	sa.SetPinned([]string{"id-web"})
	sb.SetPinned([]string{"id-db"})

	// 省略 refs → 各自钉住集合（默认目标按用户解析）
	names, unresolved := sa.Names(nil)
	if len(names) != 1 || names[0] != "web" || len(unresolved) != 0 {
		t.Fatalf("sa.Names(nil) = %v, %v; want [web]", names, unresolved)
	}
	names, _ = sb.Names(nil)
	if len(names) != 1 || names[0] != "db" {
		t.Fatalf("sb.Names(nil) = %v; want [db]", names)
	}

	// 目录只标注本会话目标主机（行形如 "- web · 标签:  🎯对话目标"）
	catA := sa.CatalogText()
	webLine, dbLine := "", ""
	for _, ln := range strings.Split(catA, "\n") {
		if strings.HasPrefix(ln, "- web ") {
			webLine = ln
		}
		if strings.HasPrefix(ln, "- db ") {
			dbLine = ln
		}
	}
	if !strings.Contains(webLine, "🎯对话目标") {
		t.Fatalf("web 行应含 🎯：%q（完整目录：%s）", webLine, catA)
	}
	if strings.Contains(dbLine, "🎯对话目标") {
		t.Fatalf("db 行不应含 🎯：%q", dbLine)
	}

	// 显式 refs 与钉住无关
	names, _ = sa.Names([]string{"db"})
	if len(names) != 1 || names[0] != "db" {
		t.Fatalf("sa.Names([db]) = %v", names)
	}
}

func TestSessionPinnedDeletedHostSkipped(t *testing.T) {
	c := openTestCore(t)
	seedHosts(t, c, "web")
	sa := c.NewSession()
	sa.SetPinned([]string{"id-web", "id-gone"})
	names, _ := sa.Names(nil)
	if len(names) != 1 || names[0] != "web" {
		t.Fatalf("Names(nil) = %v; want [web]", names)
	}
	// web 也删除后：空目标 + 目录提示未设置目标主机
	if err := c.Store().Delete("id-web"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	names, _ = sa.Names(nil)
	if len(names) != 0 {
		t.Fatalf("Names(nil) after delete = %v; want empty", names)
	}
	if cat := sa.CatalogText(); !strings.Contains(cat, "未设置目标主机") {
		t.Fatalf("catalog should warn no default target:\n%s", cat)
	}
}

func TestSetSecrets(t *testing.T) {
	c := openTestCore(t) // OpenAt 用的是 MemoryStore 占位
	mem := host.MemoryStore{"ref1": "v1"}
	c.SetSecrets(mem)
	if v, err := c.Secrets().Get("ref1"); err != nil || v != "v1" {
		t.Fatalf("Secrets().Get = %q, %v; want v1", v, err)
	}
}

func TestSessionResolveDefaultsToPinned(t *testing.T) {
	// Resolve 会建连接（此处只验证无 refs 时的目标选择路径会走到连接层：
	// 报错信息里是钉住主机名 web，而非 db）。不用共享 seedHosts——它拨
	// 10.0.0.1 依赖网络环境；这里参照 TestResolveReportsConnectFailure 用
	// 127.0.0.1:1 连接立即拒绝，确定性。
	c := openTestCore(t)
	for _, n := range []string{"web", "db"} {
		if err := c.Store().Save(&host.Host{ID: "id-" + n, Name: n, Addr: "127.0.0.1", Port: 1, User: "root"}); err != nil {
			t.Fatalf("seed %s: %v", n, err)
		}
	}
	sa := c.NewSession()
	sa.SetPinned([]string{"id-web"})
	_, _, err := sa.Resolve(nil)
	if err == nil || !strings.Contains(err.Error(), "web") {
		t.Fatalf("Resolve(nil) err = %v; want contains 'web'", err)
	}
}

// TestSessionViewFiltersByOwner NewSessionFor 视角：CatalogText 与
// Names/Resolve 只含属主自己的主机（Agent 目标解析隔离，设计 §4.3）。
func TestSessionViewFiltersByOwner(t *testing.T) {
	c := openTestCore(t)
	save := func(id, name, owner string) {
		t.Helper()
		if err := c.Store().Save(&host.Host{ID: id, Name: name, Addr: "127.0.0.1", Port: 1, User: "root", Owner: owner}); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	save("id-web", "web", "alice")
	save("id-db", "db", "bob")
	save("id-old", "legacy", "") // 存量空 owner = admin 归属

	sa := c.NewSessionFor("alice", false)
	// 目录只列 alice 的主机
	cat := sa.CatalogText()
	if strings.Contains(cat, "db") || strings.Contains(cat, "legacy") {
		t.Fatalf("alice catalog 含他人主机:\n%s", cat)
	}
	if !strings.Contains(cat, "web") {
		t.Fatalf("alice catalog 缺 web:\n%s", cat)
	}
	// 显式引用他人主机 → 无法解析（隔离边界）
	names, unresolved := sa.Names([]string{"db"})
	if len(names) != 0 || len(unresolved) != 1 || unresolved[0] != "db" {
		t.Fatalf("alice Names([db]) = %v %v; want unresolved [db]", names, unresolved)
	}
	// pinned 含他人主机 ID → 解析时被过滤（划转后旧 pinned 自然失效）
	sa.SetPinned([]string{"id-web", "id-db"})
	names, _ = sa.Names(nil)
	if len(names) != 1 || names[0] != "web" {
		t.Fatalf("sa.Names(nil) = %v; want [web]", names)
	}

	// admin 视角：全表可见（含空 owner 存量行）
	sAdmin := c.NewSessionFor("admin", true)
	if cat := sAdmin.CatalogText(); !strings.Contains(cat, "web") || !strings.Contains(cat, "db") || !strings.Contains(cat, "legacy") {
		t.Fatalf("admin catalog 应含全部:\n%s", cat)
	}
	names, _ = sAdmin.Names([]string{"all"})
	if len(names) != 3 {
		t.Fatalf("admin Names([all]) = %v; want 3", names)
	}

	// NewSession()（旧签名）保留全局语义——headless 路径不变
	sLegacy := c.NewSession()
	names, _ = sLegacy.Names([]string{"all"})
	if len(names) != 3 {
		t.Fatalf("NewSession().Names([all]) = %v; want 3（全局语义）", names)
	}
}
