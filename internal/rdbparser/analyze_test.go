package rdbparser

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFile 避免与 os.WriteFile 同名混淆的测试辅助。
func writeFile(path string, content []byte) error {
	return os.WriteFile(path, content, 0o644)
}

// 夹具由真实 redis-server 离线生成后入库（redis 3.2/5/6.2/7.2 各一份）。6 键：
// 2 string（其中 1 个 EX 3600）、1 hash、1 list、1 zset、1 set。
// 注意：夹具尚未生成齐时对应版本 Skip（缺文件 ≠ 解析失败，绝不静默放宽断言）。
func TestAnalyzeRealDumps(t *testing.T) {
	for _, v := range []string{"3.2", "5", "6.2", "7.2"} {
		t.Run("redis"+v, func(t *testing.T) {
			path := filepath.Join("testdata", "dump-redis"+v+".rdb")
			if _, err := os.Stat(path); err != nil {
				t.Skipf("夹具 %s 未生成（Docker 生成流程见任务/计划），生成后本断言全量生效", path)
			}
			r, err := Analyze(path, 10)
			if err != nil {
				t.Fatalf("Analyze(%s): %v", path, err)
			}
			if r.TotalKeys != 6 {
				t.Errorf("TotalKeys = %d, want 6 (Skipped=%d)", r.TotalKeys, r.Skipped)
			}
			if r.NoTTL != 5 || r.HasTTL != 1 {
				t.Errorf("TTL = %d/%d, want 5/1", r.NoTTL, r.HasTTL)
			}
			for _, typ := range []string{"string", "hash", "list", "sortedset", "set"} {
				if r.Types[typ] < 1 {
					t.Errorf("Types[%s] = %d, want >=1 (all: %v)", typ, r.Types[typ], r.Types)
				}
			}
			var session *PrefixStat
			for i := range r.PrefixesByKeys {
				if r.PrefixesByKeys[i].Prefix == "session:user" {
					session = &r.PrefixesByKeys[i]
				}
			}
			if session == nil || session.Keys != 2 {
				t.Errorf("prefix session:user = %+v, want 2 keys", session)
			}
		})
	}
}

func TestAnalyzeMissingFile(t *testing.T) {
	if _, err := Analyze(filepath.Join("testdata", "no-such.rdb"), 10); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestAnalyzeGarbageFails(t *testing.T) {
	// 非 RDB 内容：解析必须报错而不是 panic 或静默成功
	path := filepath.Join(t.TempDir(), "garbage.rdb")
	if err := writeFile(path, []byte("this is definitely not an rdb file, just garbage bytes 1234567890")); err != nil {
		t.Fatal(err)
	}
	if _, err := Analyze(path, 10); err == nil {
		t.Fatal("expected parse error for garbage input")
	}
}
