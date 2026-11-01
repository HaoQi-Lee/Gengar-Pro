// truncAudit8K 单测：钉死输出格式与 web 侧 webapi.truncAuditText 逐字节
// 一致（8192 rune 前缀 + 「…[+N chars]」显式省略标注）。期望串写死在此、
// 不 import webapi——两侧是独立实现，格式漂移由各自钉死测试暴露（对账
// 时比对后缀格式即可）。Task 4 审查遗留：函数自 Task 4 起无单测。
package main

import (
	"strings"
	"testing"
)

func TestTruncAudit8K(t *testing.T) {
	// 短串原样
	if got := truncAudit8K("短原文"); got != "短原文" {
		t.Fatalf("短串被改写: %q", got)
	}
	// 恰好 8192 rune：边界原样（不误加标注）
	exact := strings.Repeat("a", 8192)
	if got := truncAudit8K(exact); got != exact {
		t.Fatalf("恰好 8192 rune 应原样, got len=%d", len(got))
	}
	// 9000 rune 中文 → 8192 rune 前缀 + 与 web 侧相同的标注（rune 不切半）
	long := strings.Repeat("磁", 9000)
	want := strings.Repeat("磁", 8192) + "…[+808 chars]"
	if got := truncAudit8K(long); got != want {
		t.Fatalf("超限截断不符: got 尾=%q", got[len(got)-20:])
	}
}
