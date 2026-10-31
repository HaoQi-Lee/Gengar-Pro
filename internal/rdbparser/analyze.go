// analyze.go 门面：文件 IO + rdb.Decode 驱动 + 聚合，对外只暴露 Analyze。
// 生产 goroutine 跑 Decode 回调进 Entries；消费循环排空通道喂给 aggregator
// （Decoder 契约：一个实例只驱动一次 Decode；消费端必须排空 Entries；
// Decode 出错走 dec.Fail 关通道）。
package rdbparser

import (
	"bufio"
	"fmt"
	"os"

	"gengar-pro/internal/rdbparser/rdb"
)

// Analyze 流式解析本地 RDB 文件并返回聚合报告。topN<=0 时默认 10。
// 解析核心：内置 dongmx/rdb（rdr 同源，见 rdb/LICENCE 与包 README）。
func Analyze(path string, topN int) (*Report, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	dec := NewDecoder()
	go func() {
		// 解析核心遇畸形输入可能 panic：recover 转为错误，不能带崩进程。
		defer func() {
			if p := recover(); p != nil {
				dec.Fail(fmt.Errorf("rdb parse panic: %v", p))
			}
		}()
		if err := rdb.Decode(bufio.NewReaderSize(f, 1<<20), dec); err != nil {
			dec.Fail(fmt.Errorf("rdb decode: %w", err))
		}
	}()

	agg := newAggregator(topN)
	for e := range dec.Entries {
		agg.add(e)
	}
	if err := dec.Err(); err != nil {
		return nil, fmt.Errorf("%s 可能是不支持的 RDB 版本或已损坏: %w", path, err)
	}
	return agg.finish(dec), nil
}
