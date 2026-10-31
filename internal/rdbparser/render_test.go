package rdbparser

import (
	"strings"
	"testing"
)

func sampleReport() *Report {
	return &Report{
		RDBVersion:   11,
		RedisVersion: "7.2.4",
		TotalKeys:    1204331,
		EstBytes:     851000000,
		DBs: map[int]DBStat{
			0: {Keys: 1180000, EstBytes: 840000000},
			1: {Keys: 24331, EstBytes: 11000000},
		},
		Types: map[string]int64{
			"string": 939378, "zset": 144519, "hash": 108389, "list": 12045,
		},
		NoTTL:  542000,
		HasTTL: 662331,
		PrefixesByBytes: []PrefixStat{
			{Prefix: "session:user", Keys: 512000, EstBytes: 432000000},
			{Prefix: "cache:user", Keys: 301000, EstBytes: 210000000},
		},
		PrefixesByKeys: []PrefixStat{
			{Prefix: "session:user", Keys: 512000, EstBytes: 432000000},
			{Prefix: "cache:user", Keys: 301000, EstBytes: 210000000},
		},
		BigKeys: []KeyStat{
			{Key: "user:rank", Type: "zset", EstBytes: 8600000, NumOfElem: 120000},
			{Key: "user:1001", Type: "hash", EstBytes: 6400000, NumOfElem: 88},
		},
	}
}

func TestRenderContainsCoreSections(t *testing.T) {
	out := Render("/var/lib/redis/dump.rdb", 119537664, 1761763200, sampleReport())
	for _, want := range []string{
		"=== rdb: /var/lib/redis/dump.rdb",
		"redis 7.2.4",
		"总键数 1204331",
		"内存估算",
		"DB0",
		"string",
		"session:user",
		"cache:user",
		"永久键(无TTL): 45%",
		"user:rank",
		"内存为估算值",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, "被跳过") {
		t.Errorf("skipped=0 的报告不应出现跳过警告:\n%s", out)
	}
}

func TestRenderSkippedWarning(t *testing.T) {
	r := sampleReport()
	r.Skipped = 3
	out := Render("/x/dump.rdb", 10, 0, r)
	if !strings.Contains(out, "3 个键因不支持的编码被跳过") {
		t.Errorf("missing skipped warning:\n%s", out)
	}
	if !strings.Contains(out, "mtime 未知") {
		t.Errorf("missing mtime 未知:\n%s", out)
	}
}

// 空 Report 冒烟：Render 不 panic，且不输出分布类段落。
func TestRenderEmptyReport(t *testing.T) {
	out := Render("/x/empty.rdb", 0, 0, &Report{})
	for _, banned := range []string{"DB 分布", "类型:", "Top 前缀", "Top 大键", "被跳过"} {
		if strings.Contains(out, banned) {
			t.Errorf("empty report should not contain %q:\n%s", banned, out)
		}
	}
	if !strings.Contains(out, "总键数 0") {
		t.Errorf("empty report should still have 总键数 0:\n%s", out)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[uint64]string{
		0:         "0B",
		512:       "512B",
		1023:      "1023B",
		1024:      "1.0KB",
		2048:      "2.0KB",
		5 << 20:   "5.0MB",
		3 << 30:   "3.0GB",
		114000000: "108.7MB",
	}
	for in, want := range cases {
		if got := HumanBytes(in); got != want {
			t.Errorf("HumanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}
