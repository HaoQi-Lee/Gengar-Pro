// Package audit 记录 gengar-pro 的操作审计流水：谁（会话）在何时对
// 哪台主机执行了什么工具、是否经过确认、结果如何。JSONL 追加写，
// 每行一个完整 JSON 对象，方便 jq / grep 事后分析。
// 轮转（评审 ③-3）：按天分片（audit-YYYY-MM-DD.jsonl），保留 N 天
// （默认 30），过期分片启动时删除；写失败计数暴露给 /api/health。
package audit

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Entry struct {
	TS      string `json:"ts"`
	Session string `json:"session"`
	// User 是 web 多用户下的操作者标识；headless 单机场景为空（omitempty
	// 时不出现在 JSON，旧流水解析不受影响）。
	User string `json:"user,omitempty"`
	Tool string `json:"tool"`
	// Input 为 nil 时序列化为 null，消费方按"未记录输入"处理。
	Input    json.RawMessage `json:"input"`
	Hosts    []string        `json:"hosts"`
	Approved bool            `json:"approved"`
	IsError  bool            `json:"is_error"`
	Output   string          `json:"output,omitempty"`
}

// Logger 把 Entry 追加写入按天分片的 JSONL 文件。并发安全。
type Logger struct {
	mu *sync.Mutex
	// path 是不含日期后缀的基础路径（如 <data>/audit）；分片文件为
	// audit-YYYY-MM-DD.jsonl。历史固定名 audit.jsonl 兼容读取但不续写。
	path string
	f    *os.File
	w    *bufio.Writer
	day  string // 当前分片的 YYYY-MM-DD

	writeFails int // 写失败次数（/api/health 暴露）
}

// Open 打开轮转式审计。path 为历史固定路径（<data>/audit.jsonl）：
// 新分片写 <dir>/audit-<date>.jsonl。保留天数 keepDays（<=0 取默认 30）。
func Open(path string) (*Logger, error) {
	dir := filepath.Dir(path)
	// 0o600：审计记录含命令输入，可能带秘密，仅属主可读。
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	l := &Logger{mu: &sync.Mutex{}, path: filepath.Join(dir, base)}
	if err := l.rotateLocked(); err != nil {
		return nil, err
	}
	return l, nil
}

// keepDaysDefault 保留天数默认值。
const keepDaysDefault = 30

// rotateLocked 按当天日期开新分片（须持 l.mu）。跨天首写触发轮转；
// 同时清理过期分片。
func (l *Logger) rotateLocked() error {
	day := time.Now().Format("2006-01-02")
	if l.f != nil && l.day == day {
		return nil
	}
	if l.f != nil {
		_ = l.f.Close()
		l.f = nil
	}
	l.day = day
	shard := l.shardPath(day)
	f, err := os.OpenFile(shard, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	l.f = f
	l.w = bufio.NewWriter(f)
	l.pruneLocked()
	return nil
}

// shardPath 返回某天的分片文件路径。
func (l *Logger) shardPath(day string) string {
	return l.path + "-" + day + ".jsonl"
}

// pruneLocked 删除超过保留期的分片（须持 l.mu）。
func (l *Logger) pruneLocked() {
	keep := keepDaysDefault
	entries, err := filepath.Glob(l.path + "-*.jsonl")
	if err != nil || len(entries) == 0 {
		return
	}
	sort.Strings(entries) // audit-YYYY-MM-DD 字典序即时间序
	cutoff := time.Now().AddDate(0, 0, -keep).Format("2006-01-02")
	for _, e := range entries {
		day := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(e), filepath.Base(l.path)+"-"), ".jsonl")
		if day < cutoff {
			_ = os.Remove(e)
		}
	}
}

// Log 序列化并追加一条记录（立即 Flush 并 fsync——审计宁可慢不可丢）。
// 跨天首写自动轮转分片。
func (l *Logger) Log(e Entry) error {
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.rotateLocked(); err != nil {
		l.writeFails++
		return err
	}
	if _, err := l.w.Write(append(raw, '\n')); err != nil {
		l.writeFails++
		return err
	}
	if err := l.w.Flush(); err != nil {
		l.writeFails++
		return err
	}
	if err := l.f.Sync(); err != nil {
		l.writeFails++
		return err
	}
	return nil
}

// WriteFails 返回累计写失败次数（/api/health 暴露，运维告警信号）。
func (l *Logger) WriteFails() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.writeFails
}

// CurrentShard 返回当前分片路径（health/排错用）。
func (l *Logger) CurrentShard() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.shardPath(l.day)
}

func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	if err := l.w.Flush(); err != nil {
		_ = l.f.Close()
		return err
	}
	return l.f.Close()
}

// String 给 health 输出用。
func (e Entry) String() string {
	return fmt.Sprintf("%s %s %s", e.TS, e.User, e.Tool)
}
