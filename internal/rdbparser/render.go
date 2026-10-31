// render.go 渲染层：把 Report 渲染为面向 LLM 阅读的紧凑文本报告
// （≤2KB 量级）。path/sizeBytes/mtimeUnix 由插件侧（redis_rdb 工具）
// 探测远端文件后传入。
package rdbparser

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Render 渲染分析报告。path/sizeBytes/mtimeUnix 由插件侧探测提供
// （远端文件属性），其余来自聚合报告。输出面向 LLM 阅读的紧凑文本。
func Render(path string, sizeBytes, mtimeUnix int64, r *Report) string {
	var b strings.Builder

	mtime := "未知"
	if mtimeUnix > 0 {
		mtime = time.Unix(mtimeUnix, 0).Format("2006-01-02 15:04")
	}
	fmt.Fprintf(&b, "=== rdb: %s (%s, mtime %s) ===\n", path, HumanBytes(uint64(sizeBytes)), mtime)

	ver := r.RedisVersion
	if ver == "" {
		ver = "?"
	}
	fmt.Fprintf(&b, "redis %s · rdb v%d · 总键数 %d · 内存估算 %s\n",
		ver, r.RDBVersion, r.TotalKeys, HumanBytes(r.EstBytes))

	// DB 分布（DB 编号升序）
	dbs := make([]int, 0, len(r.DBs))
	for db := range r.DBs {
		dbs = append(dbs, db)
	}
	sort.Ints(dbs)
	parts := make([]string, 0, len(dbs))
	for _, db := range dbs {
		st := r.DBs[db]
		pct := 100 * st.Keys / max(r.TotalKeys, 1)
		parts = append(parts, fmt.Sprintf("DB%d %d键/%s (%d%%)", db, st.Keys, HumanBytes(st.EstBytes), pct))
	}
	if len(parts) > 0 {
		fmt.Fprintf(&b, "DB 分布: %s\n", strings.Join(parts, " · "))
	}

	// 类型分布（数量降序）
	if len(r.Types) > 0 {
		types := make([]string, 0, len(r.Types))
		for typ := range r.Types {
			types = append(types, typ)
		}
		sort.Slice(types, func(i, j int) bool { return r.Types[types[i]] > r.Types[types[j]] })
		tp := make([]string, 0, len(types))
		for _, typ := range types {
			pct := 100 * r.Types[typ] / max(r.TotalKeys, 1)
			tp = append(tp, fmt.Sprintf("%s %d%%", typ, pct))
		}
		fmt.Fprintf(&b, "类型: %s\n", strings.Join(tp, " / "))
	}

	// Top 前缀
	if len(r.PrefixesByBytes) > 0 {
		pp := make([]string, 0, len(r.PrefixesByBytes))
		for _, ps := range r.PrefixesByBytes {
			pp = append(pp, fmt.Sprintf("%s %s·%d键", ps.Prefix, HumanBytes(ps.EstBytes), ps.Keys))
		}
		fmt.Fprintf(&b, "Top 前缀(按内存): %s\n", strings.Join(pp, " | "))
	}
	if len(r.PrefixesByKeys) > 0 {
		pp := make([]string, 0, len(r.PrefixesByKeys))
		for _, ps := range r.PrefixesByKeys {
			pp = append(pp, fmt.Sprintf("%s %d键/%s", ps.Prefix, ps.Keys, HumanBytes(ps.EstBytes)))
		}
		fmt.Fprintf(&b, "Top 前缀(按键数): %s\n", strings.Join(pp, " | "))
	}

	// TTL
	if r.NoTTL > 0 {
		pct := 100 * r.NoTTL / max(r.TotalKeys, 1)
		fmt.Fprintf(&b, "永久键(无TTL): %d%% (%d) —— ⚠️ 缓存类键建议设 TTL\n", pct, r.NoTTL)
	}

	// Top 大键
	if len(r.BigKeys) > 0 {
		kp := make([]string, 0, len(r.BigKeys))
		for _, ks := range r.BigKeys {
			kp = append(kp, fmt.Sprintf("%s %s %s", ks.Type, ks.Key, HumanBytes(ks.EstBytes)))
		}
		fmt.Fprintf(&b, "Top 大键: %s\n", strings.Join(kp, " | "))
	}

	if r.Skipped > 0 {
		fmt.Fprintf(&b, "⚠️ %d 个键因不支持的编码被跳过\n", r.Skipped)
	}

	b.WriteString("(内存为估算值，算法同 rdr/MEMORY USAGE 近似)\n")
	return b.String()
}

// HumanBytes 人类可读字节数（1 位小数）。
func HumanBytes(n uint64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}
