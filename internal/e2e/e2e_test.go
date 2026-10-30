//go:build e2e

// Package e2e 是 gengar-pro 的端到端套件：桩 LLM（SSE 脚本回放）+
// Docker sshd 真机 + headless 二进制驱动，断言 stdout、audit.jsonl 与
// 数据目录文件（记忆）。
//
// 运行：go test -tags e2e ./internal/e2e/ -v
// 前置：Docker 可用（不可用 → 用例 Skip；桩/断言单测仍运行）。
// llm=real 的用例另需 ANTHROPIC_API_KEY。
package e2e

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestMain：headless 二进制的临时目录是进程级缓存（sync.Once 编译一次、
// 全部用例复用同一文件），生命周期长于任何单个用例，不能挂 t.Cleanup
// （首用例结束就会删掉后续用例仍要复用的二进制）——进程退出时统一清理。
func TestMain(m *testing.M) {
	code := m.Run()
	if headlessBinDir != "" {
		_ = os.RemoveAll(headlessBinDir)
	}
	os.Exit(code)
}

// TestE2ECases 遍历 testcases/*.yaml 逐用例驱动。
func TestE2ECases(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("testcases", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no testcases/*.yaml found")
	}
	for _, p := range paths {
		p := p
		t.Run(filepath.Base(p), func(t *testing.T) {
			c, err := loadCase(p)
			if err != nil {
				t.Fatal(err)
			}
			runCase(t, c)
		})
	}
}

// runCase 是一个用例的完整生命周期：LLM 源（桩/真机）→ 目标机（Docker
// sshd）→ 播种 → 逐轮 headless 驱动（runs>1 = 多进程共用 DataDir，跨
// 会话正是 E05 的被测点；每轮独立起桩，脚本从头回放）→ 断言对合并
// stdout + 全量 audit + 数据目录求值。
func runCase(t *testing.T, c *e2eCase) {
	apiKey := "e2e-key"
	if c.LLM == "real" {
		key := os.Getenv("ANTHROPIC_API_KEY")
		if key == "" {
			t.Skip("llm=real requires ANTHROPIC_API_KEY")
		}
		apiKey = key
	}

	hosts := startHosts(t, c.Hosts) // Docker 缺失 → 此处统一 Skip

	dataDir := t.TempDir()
	seed := make([]SeedHost, 0, len(hosts))
	for i, h := range hosts {
		seed = append(seed, SeedHost{Name: h.Name, Addr: h.Addr, Port: h.Port, Tags: c.Tags[i]})
	}
	seedHosts(t, dataDir, seed)

	autoYes := c.AutoYes == nil || *c.AutoYes
	var stdout string
	for _, r := range c.Runs {
		baseURL := ""
		if c.LLM == "stub" {
			stub := startStubLLM(t, scriptToTurns(r.Script))
			baseURL = stub.URL()
		}
		stdout += runHeadless(t, dataDir, baseURL, apiKey, r.Inputs, 5*time.Minute, autoYes)
	}
	entries := readAudit(t, dataDir)

	for _, a := range c.Assertions {
		if err := evalAssertion(a, stdout, entries, dataDir); err != nil {
			t.Errorf("assertion %s: %v", a.Type, err)
		}
	}
}
