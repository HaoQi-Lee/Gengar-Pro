package host

import "testing"

func sampleHosts() []*Host {
	return []*Host{
		{ID: "h1", Name: "web-01", Tags: []string{"web", "prod"}},
		{ID: "h2", Name: "web-02", Tags: []string{"web", "prod"}},
		{ID: "h3", Name: "db-01", Tags: []string{"db", "prod"}},
	}
}

func TestResolveByName(t *testing.T) {
	got, unresolved := Resolve(sampleHosts(), []string{"web-01"})
	if len(unresolved) != 0 || len(got) != 1 || got[0].Name != "web-01" {
		t.Fatalf("got %v unresolved %v", got, unresolved)
	}
}

func TestResolveByTag(t *testing.T) {
	got, unresolved := Resolve(sampleHosts(), []string{"web"})
	if len(unresolved) != 0 || len(got) != 2 {
		t.Fatalf("expected 2 web hosts, got %v unresolved %v", got, unresolved)
	}
}

func TestResolveAll(t *testing.T) {
	got, _ := Resolve(sampleHosts(), []string{"all"})
	if len(got) != 3 {
		t.Fatalf("all should match every host, got %d", len(got))
	}
}

func TestResolveMixedAndUnresolved(t *testing.T) {
	got, unresolved := Resolve(sampleHosts(), []string{"db-01", "web", "nope"})
	if len(got) != 3 || len(unresolved) != 1 || unresolved[0] != "nope" {
		t.Fatalf("got %v unresolved %v", got, unresolved)
	}
}

func TestResolveDedup(t *testing.T) {
	// web-01 同时被名字和标签命中，只出现一次
	got, _ := Resolve(sampleHosts(), []string{"web-01", "web"})
	if len(got) != 2 {
		t.Fatalf("expected dedup to 2, got %d", len(got))
	}
}

func TestResolveNameWinsOverTag(t *testing.T) {
	// 有主机恰好叫 "web" 时，名字优先于同名标签
	hosts := append(sampleHosts(), &Host{ID: "h4", Name: "web"})
	got, _ := Resolve(hosts, []string{"web"})
	if len(got) != 1 || got[0].ID != "h4" {
		t.Fatalf("name should win, got %v", got)
	}
}
