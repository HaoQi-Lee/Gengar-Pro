// aggregate.go 聚合层：消费 Entry 流累计出 Report（总键数/内存估算/DB 分布/
// 类型分布/TTL 统计/前缀 Top-N/大键 Top-N）。纯内存，无 IO；Analyze 负责把
// decoder 的 Entries 通道接到 aggregator.add。
package rdbparser

import (
	"sort"
	"strings"
)

// Report 是一份 RDB 的聚合结果（Analyze 的输出）。
type Report struct {
	RDBVersion      int
	RedisVersion    string
	DumpCtime       int64 // RDB 自记录的生成时间（unix 秒，可能为 0）
	UsedMemSnapshot int64 // RDB 自记录的 dump 时 used-mem（字节）
	TotalKeys       int64
	EstBytes        uint64 // 全部键内存估算（近似，同 rdr 算法）
	Skipped         int64  // 不支持编码被跳过的键数
	DBs             map[int]DBStat
	Types           map[string]int64
	NoTTL           int64
	HasTTL          int64
	PrefixesByBytes []PrefixStat
	PrefixesByKeys  []PrefixStat
	BigKeys         []KeyStat
}

// DBStat 是单个 DB 的键数与内存估算。
type DBStat struct {
	Keys     int64
	EstBytes uint64
}

// PrefixStat 是一个键前缀的分组的键数与内存估算。
type PrefixStat struct {
	Prefix   string
	Keys     int64
	EstBytes uint64
}

// KeyStat 是按内存排序的单键条目（大键 Top-N 的元素）。
type KeyStat struct {
	Key       string
	Type      string
	EstBytes  uint64
	NumOfElem uint64
}

// PrefixOf 取键前缀：按 ':' 切分取至多两段。
// session:user:1001 → session:user；无分隔符的键自成一组。
func PrefixOf(key string) string {
	i := strings.Index(key, ":")
	if i < 0 {
		return key
	}
	rest := key[i+1:]
	j := strings.Index(rest, ":")
	if j < 0 {
		return key
	}
	return key[:i+1+j]
}

// aggregator 消费 Entry 流累计统计。纯内存，无 IO。
// finish 后实例冻结：再调用 add/finish 会 panic（防止报告冻结后又混入数据）。
type aggregator struct {
	rep    Report
	prefix map[string]*PrefixStat
	big    []KeyStat
	topN   int
	done   bool
}

func newAggregator(topN int) *aggregator {
	if topN <= 0 { // 非法值收敛为默认 10
		topN = 10
	}
	return &aggregator{
		rep:    Report{DBs: map[int]DBStat{}, Types: map[string]int64{}},
		prefix: map[string]*PrefixStat{},
		topN:   topN,
	}
}

// add 累计一条 Entry。Expiry 单位是毫秒，聚合只判 ==0（永久键），不换算。
// finish 后调用会 panic。
func (a *aggregator) add(e *Entry) {
	if a.done {
		panic("aggregator: use after finish")
	}
	a.rep.TotalKeys++
	a.rep.EstBytes += e.Bytes

	db := a.rep.DBs[e.DB]
	db.Keys++
	db.EstBytes += e.Bytes
	a.rep.DBs[e.DB] = db

	a.rep.Types[e.Type]++

	if e.Expiry == 0 {
		a.rep.NoTTL++
	} else {
		a.rep.HasTTL++
	}

	p := PrefixOf(e.Key)
	ps, ok := a.prefix[p]
	if !ok {
		ps = &PrefixStat{Prefix: p}
		a.prefix[p] = ps
	}
	ps.Keys++
	ps.EstBytes += e.Bytes

	a.big = append(a.big, KeyStat{
		Key: e.Key, Type: e.Type, EstBytes: e.Bytes, NumOfElem: e.NumOfElem,
	})
}

// finish 排序截断并冻结报告。dec 可为 nil（测试用；元数据留零值）。
// 重复调用或冻结后再 add 会 panic。
func (a *aggregator) finish(dec *Decoder) *Report {
	if a.done {
		panic("aggregator: use after finish")
	}
	if dec != nil {
		a.rep.RDBVersion = dec.RDBVersion()
		a.rep.RedisVersion = dec.RedisVersion()
		a.rep.DumpCtime = dec.Timestamp()
		a.rep.UsedMemSnapshot = dec.UsedMem()
		a.rep.Skipped = dec.SkippedCount()
	}

	byBytes := make([]PrefixStat, 0, len(a.prefix))
	for _, ps := range a.prefix {
		byBytes = append(byBytes, *ps)
	}
	byKeys := make([]PrefixStat, len(byBytes))
	copy(byKeys, byBytes)

	sort.Slice(byBytes, func(i, j int) bool {
		if byBytes[i].EstBytes != byBytes[j].EstBytes {
			return byBytes[i].EstBytes > byBytes[j].EstBytes
		}
		return byBytes[i].Prefix < byBytes[j].Prefix
	})
	sort.Slice(byKeys, func(i, j int) bool {
		if byKeys[i].Keys != byKeys[j].Keys {
			return byKeys[i].Keys > byKeys[j].Keys
		}
		return byKeys[i].Prefix < byKeys[j].Prefix
	})
	if len(byBytes) > a.topN {
		byBytes = byBytes[:a.topN]
	}
	if len(byKeys) > a.topN {
		byKeys = byKeys[:a.topN]
	}
	a.rep.PrefixesByBytes = byBytes
	a.rep.PrefixesByKeys = byKeys

	sort.Slice(a.big, func(i, j int) bool {
		if a.big[i].EstBytes != a.big[j].EstBytes {
			return a.big[i].EstBytes > a.big[j].EstBytes
		}
		return a.big[i].Key < a.big[j].Key
	})
	if len(a.big) > a.topN {
		a.big = a.big[:a.topN]
	}
	a.rep.BigKeys = a.big
	a.done = true
	return &a.rep
}
