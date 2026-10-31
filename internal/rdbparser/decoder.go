// decoder.go 生产端：把 dongmx/rdb 的回调流转换为 Entry 通道。
// 移植自 github.com/xueqiu/rdr/decoder（Apache-2.0，见同目录 LICENSE）。
// 相对母本的适配点：
//  1. Entry 增加 DB / Expiry（rdr 不追踪；报告需要 DB 分布与 TTL 统计）
//  2. 覆盖 StartDatabase 回调追踪当前 DB（vendored 接口的方法名即 StartDatabase，
//     而非母本侧惯称的 SelectDB）
//  3. StartSet 修正 Type 为 "set"（母本直接复用 StartHash，set 会被错记成 hash）
//  4. 未知编码不再 panic，改为丢弃该键并计数 skipped（失败不废整份报告）
//  5. Aux 额外抓取 redis-ver
//  6. 新增 Fail/Err：rdb.Decode 出错时生产方回传错误并关闭通道
//     （Decode 出错不会触发 EndRDB）
//  7. 去掉母本的 fmt.Fprintln(os.Stderr) 噪音
//  8. 关闭通道必须幂等（EndRDB 与 Fail 都可能关）：closeOnce；
//     parseErr 由 failOnce 保护
package rdbparser

import (
	"strconv"
	"sync"

	"gengar-pro/internal/rdbparser/rdb"
	"gengar-pro/internal/rdbparser/rdb/nopdecoder"
)

// Entry 是单个 Redis 键的解析结果。
type Entry struct {
	Key                string
	DB                 int   // 适配：rdr 不追踪，报告需要 DB 分布
	Expiry             int64 // unix 毫秒；0 = 永久键。适配：rdr 不追踪
	Bytes              uint64
	Type               string
	NumOfElem          uint64
	LenOfLargestElem   uint64
	FieldOfLargestElem string
}

// Decoder 实现 rdb.Decoder 回调（nopdecoder 兜底未覆盖项）。
//
// 注意（防误用）：
//   - 一个实例只能驱动一次 rdb.Decode：Entries 关闭后再走回调会向已关闭
//     通道发送而 panic
//   - 消费端必须排空 Entries：提前退出会让生产 goroutine 永久阻塞在
//     通道发送上
type Decoder struct {
	Entries chan *Entry
	m       MemProfiler

	usedMem  int64
	ctime    int64
	redisVer string
	skipped  int64
	rdbVer   int

	currentDB    int
	currentInfo  *rdb.Info
	currentEntry *Entry
	skipCurrent  bool

	parseErr  error
	failOnce  sync.Once
	closeOnce sync.Once

	nopdecoder.NopDecoder
}

// NewDecoder 创建一个解码器；消费端 range Entries 直到通道关闭。
func NewDecoder() *Decoder {
	return &Decoder{
		Entries: make(chan *Entry, 1024),
		m:       MemProfiler{},
	}
}

func (d *Decoder) sendEntry() {
	if d.currentEntry != nil {
		d.currentEntry.DB = d.currentDB
		d.Entries <- d.currentEntry
	}
	d.currentEntry = nil
}

// --- 元数据访问（消费端在通道关闭后读取） ---

func (d *Decoder) RDBVersion() int      { return d.rdbVer }
func (d *Decoder) Timestamp() int64     { return d.ctime }
func (d *Decoder) UsedMem() int64       { return d.usedMem }
func (d *Decoder) RedisVersion() string { return d.redisVer }
func (d *Decoder) SkippedCount() int64  { return d.skipped }

// Fail 由生产方在 rdb.Decode 出错时调用：记录错误并关闭通道，
// 让消费端 range 结束（Decode 出错不会触发 EndRDB）。
func (d *Decoder) Fail(err error) {
	d.failOnce.Do(func() {
		d.parseErr = err
		d.closeEntries()
	})
}

// Err 返回 Fail 记录的解析错误；nil 表示解析正常走完。
func (d *Decoder) Err() error { return d.parseErr }

// closeEntries 幂等关闭通道：EndRDB 与 Fail 都可能触发。
func (d *Decoder) closeEntries() {
	d.closeOnce.Do(func() { close(d.Entries) })
}

// --- 回调 ---

func (d *Decoder) StartRDB(ver int) { d.rdbVer = ver }

// StartDatabase 追踪当前 DB（vendored rdb.Decoder 接口在 SelectDB 操作码处回调本方法）。
func (d *Decoder) StartDatabase(n int) { d.currentDB = n }

func (d *Decoder) Aux(key, value []byte) {
	switch string(key) {
	case "ctime":
		if n, err := strconv.ParseInt(string(value), 10, 64); err == nil {
			d.ctime = n
		}
	case "used-mem":
		if n, err := strconv.ParseInt(string(value), 10, 64); err == nil {
			d.usedMem = n
		}
	case "redis-ver": // 适配：rdr 不抓取
		d.redisVer = string(value)
	}
}

func (d *Decoder) Set(key, value []byte, expiry int64, info *rdb.Info) {
	d.Entries <- &Entry{
		Key:       string(key),
		DB:        d.currentDB,
		Expiry:    expiry,
		Bytes:     d.m.TopLevelObjOverhead(key, expiry) + d.m.SizeofString(value),
		Type:      "string",
		NumOfElem: d.m.ElemLen(value),
	}
}

func (d *Decoder) StartHash(key []byte, length, expiry int64, info *rdb.Info) {
	bytes := d.m.TopLevelObjOverhead(key, expiry)
	switch {
	case info.SizeOfValue > 0:
		bytes += uint64(info.SizeOfValue)
	case info.Encoding == "hashtable":
		bytes += d.m.HashtableOverhead(uint64(length))
	default:
		// 适配：rdr 在此 panic；改为跳过该键并计数
		d.skipped++
		d.skipCurrent = true
		d.currentEntry = nil
		return
	}
	d.currentInfo = info
	d.currentEntry = &Entry{
		Key:       string(key),
		Expiry:    expiry,
		Bytes:     bytes,
		Type:      "hash",
		NumOfElem: uint64(length),
	}
}

func (d *Decoder) Hset(key, field, value []byte) {
	if d.skipCurrent {
		return
	}
	e := d.currentEntry
	lenOfElem := d.m.ElemLen(field) + d.m.ElemLen(value)
	if lenOfElem > e.LenOfLargestElem {
		e.FieldOfLargestElem = string(field)
		e.LenOfLargestElem = lenOfElem
	}
	if d.currentInfo.Encoding == "hashtable" {
		e.Bytes += d.m.SizeofString(field)
		e.Bytes += d.m.SizeofString(value)
		e.Bytes += d.m.HashtableEntryOverhead()
		if d.rdbVer < 8 {
			e.Bytes += 2 * d.m.RobjOverhead()
		}
	}
}

func (d *Decoder) EndHash(key []byte) {
	if d.skipCurrent {
		d.skipCurrent = false
		return
	}
	d.sendEntry()
}

// StartSet 适配：rdr 直接复用 StartHash（Type 会错记为 hash），这里改回 set。
func (d *Decoder) StartSet(key []byte, cardinality, expiry int64, info *rdb.Info) {
	d.StartHash(key, cardinality, expiry, info)
	if d.currentEntry != nil {
		d.currentEntry.Type = "set"
	}
}

func (d *Decoder) Sadd(key, member []byte) {
	if d.skipCurrent {
		return
	}
	e := d.currentEntry
	if lenOfElem := d.m.ElemLen(member); lenOfElem > e.LenOfLargestElem {
		e.FieldOfLargestElem = string(member)
		e.LenOfLargestElem = lenOfElem
	}
	if d.currentInfo.Encoding == "hashtable" {
		e.Bytes += d.m.SizeofString(member)
		e.Bytes += d.m.HashtableEntryOverhead()
		if d.rdbVer < 8 {
			e.Bytes += d.m.RobjOverhead()
		}
	}
}

func (d *Decoder) EndSet(key []byte) { d.EndHash(key) }

func (d *Decoder) StartList(key []byte, length, expiry int64, info *rdb.Info) {
	d.currentInfo = info
	d.currentEntry = &Entry{
		Key:    string(key),
		Expiry: expiry,
		Bytes:  d.m.TopLevelObjOverhead(key, expiry),
		Type:   "list",
	}
}

func (d *Decoder) Rpush(key, value []byte) {
	if d.skipCurrent {
		return
	}
	e := d.currentEntry
	e.NumOfElem++
	switch d.currentInfo.Encoding {
	case "quicklist", "ziplist":
		e.Bytes += d.m.ZiplistEntryOverhead(value)
	case "linkedlist":
		sizeInList := uint64(0)
		if _, err := strconv.ParseInt(string(value), 10, 32); err != nil {
			sizeInList = d.m.SizeofString(value)
		}
		e.Bytes += d.m.LinkedListEntryOverhead() + sizeInList
		if d.rdbVer < 8 {
			e.Bytes += d.m.RobjOverhead()
		}
	default:
		// 适配：rdr 在此 panic
		d.skipped++
		d.skipCurrent = true
		d.currentEntry = nil
		return
	}
	if lenOfElem := d.m.ElemLen(value); lenOfElem > e.LenOfLargestElem {
		e.FieldOfLargestElem = string(value)
		e.LenOfLargestElem = lenOfElem
	}
}

func (d *Decoder) EndList(key []byte) {
	if d.skipCurrent {
		d.skipCurrent = false
		return
	}
	e := d.currentEntry
	switch d.currentInfo.Encoding {
	case "quicklist":
		e.Bytes += d.m.QuicklistOverhead(d.currentInfo.Zips)
		e.Bytes += d.m.ZiplistHeaderOverhead() * d.currentInfo.Zips
	case "ziplist":
		e.Bytes += d.m.ZiplistHeaderOverhead()
	case "linkedlist":
		e.Bytes += d.m.LinkedlistOverhead()
	default:
		// 适配：rdr 在此 panic
		d.skipped++
		d.currentEntry = nil
		return
	}
	d.sendEntry()
}

func (d *Decoder) StartZSet(key []byte, cardinality, expiry int64, info *rdb.Info) {
	bytes := d.m.TopLevelObjOverhead(key, expiry)
	d.currentInfo = info
	switch {
	case info.SizeOfValue > 0:
		bytes += uint64(info.SizeOfValue)
	case info.Encoding == "skiplist":
		bytes += d.m.SkiplistOverhead(uint64(cardinality))
	default:
		// 适配：rdr 在此 panic
		d.skipped++
		d.skipCurrent = true
		d.currentEntry = nil
		return
	}
	d.currentEntry = &Entry{
		Key:       string(key),
		Expiry:    expiry,
		Bytes:     bytes,
		Type:      "sortedset",
		NumOfElem: uint64(cardinality),
	}
}

func (d *Decoder) Zadd(key []byte, score float64, member []byte) {
	if d.skipCurrent {
		return
	}
	e := d.currentEntry
	if lenOfElem := d.m.ElemLen(member); lenOfElem > e.LenOfLargestElem {
		e.FieldOfLargestElem = string(member)
		e.LenOfLargestElem = lenOfElem
	}
	if d.currentInfo.Encoding == "skiplist" {
		e.Bytes += 8 // sizeof(score)
		e.Bytes += d.m.SizeofString(member)
		e.Bytes += d.m.SkiplistEntryOverhead()
		if d.rdbVer < 8 {
			e.Bytes += d.m.RobjOverhead()
		}
	}
}

func (d *Decoder) EndZSet(key []byte) { d.EndHash(key) }

func (d *Decoder) StartStream(key []byte, cardinality, expiry int64, info *rdb.Info) {
	bytes := d.m.TopLevelObjOverhead(key, expiry)
	bytes += d.m.StreamOverhead()
	bytes += d.m.SizeofStreamRadixTree(uint64(cardinality))
	d.currentInfo = info
	d.currentEntry = &Entry{
		Key:    string(key),
		Expiry: expiry,
		Bytes:  bytes,
		Type:   "stream",
	}
}

func (d *Decoder) Xadd(key, id, listpack []byte) {
	if d.skipCurrent {
		return
	}
	d.currentEntry.Bytes += d.m.mallocOverhead(uint64(len(listpack)))
}

func (d *Decoder) EndStream(key []byte, items uint64, lastEntryID string, cgroupsData rdb.StreamGroups) {
	if d.skipCurrent {
		d.skipCurrent = false
		return
	}
	e := d.currentEntry
	for _, cg := range cgroupsData {
		pendingLength := uint64(len(cg.Pending))
		e.Bytes += d.m.SizeofStreamRadixTree(pendingLength)
		e.Bytes += d.m.StreamNACK(pendingLength)
		for _, c := range cg.Consumers {
			e.Bytes += d.m.StreamConsumer(c.Name)
			e.Bytes += d.m.SizeofStreamRadixTree(pendingLength)
		}
	}
	d.sendEntry()
}

// EndRDB 解析正常走完，关闭通道结束消费端 range。
func (d *Decoder) EndRDB() { d.closeEntries() }
