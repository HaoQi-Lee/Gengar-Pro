package rdbparser

import "testing"

func TestPrefixOf(t *testing.T) {
	cases := map[string]string{
		"session:user:1001": "session:user",
		"session:user":      "session:user",
		"single":            "single",
		"a:b:c:d":           "a:b",
		"":                  "",
	}
	for in, want := range cases {
		if got := PrefixOf(in); got != want {
			t.Errorf("PrefixOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAggregatorCounts(t *testing.T) {
	a := newAggregator(10)
	a.add(&Entry{Key: "s:u:1", Expiry: 0, Bytes: 100, Type: "string"})
	a.add(&Entry{Key: "s:u:2", Expiry: 1700000000000, Bytes: 50, Type: "string"})
	a.add(&Entry{Key: "h:1", Expiry: 0, Bytes: 500, Type: "hash", NumOfElem: 2})
	r := a.finish(nil)

	if r.TotalKeys != 3 {
		t.Errorf("TotalKeys = %d, want 3", r.TotalKeys)
	}
	if r.EstBytes != 650 {
		t.Errorf("EstBytes = %d, want 650", r.EstBytes)
	}
	if r.NoTTL != 2 || r.HasTTL != 1 {
		t.Errorf("TTL = %d/%d, want 2/1", r.NoTTL, r.HasTTL)
	}
	if r.Types["string"] != 2 || r.Types["hash"] != 1 {
		t.Errorf("Types = %v", r.Types)
	}
	if r.DBs[0].Keys != 3 {
		t.Errorf("DBs[0] = %v, want 3 keys", r.DBs[0])
	}
}

func TestAggregatorDBSplit(t *testing.T) {
	a := newAggregator(10)
	a.add(&Entry{Key: "a", DB: 0, Bytes: 10, Type: "string"})
	a.add(&Entry{Key: "b", DB: 2, Bytes: 20, Type: "string"})
	r := a.finish(nil)
	if r.DBs[0].Keys != 1 || r.DBs[2].Keys != 1 || r.DBs[2].EstBytes != 20 {
		t.Errorf("DBs = %v", r.DBs)
	}
}

func TestAggregatorPrefixTopN(t *testing.T) {
	// 前缀取前两段（设计 §5.2）：big:a:1 与 big:a:2 同组到 "big:a"。
	// （原计划的测试数据 "big:1"/"big:2" 是两段键，按本语义各自成组，
	// 与期望 "big" 矛盾，故改为三段键。）
	a := newAggregator(2)
	a.add(&Entry{Key: "big:a:1", Bytes: 300, Type: "string"})
	a.add(&Entry{Key: "big:a:2", Bytes: 100, Type: "string"})
	a.add(&Entry{Key: "mid:b:1", Bytes: 150, Type: "string"})
	a.add(&Entry{Key: "small:c:1", Bytes: 5, Type: "string"})
	r := a.finish(nil)

	if len(r.PrefixesByBytes) != 2 {
		t.Fatalf("len(PrefixesByBytes) = %d, want 2", len(r.PrefixesByBytes))
	}
	if r.PrefixesByBytes[0].Prefix != "big:a" || r.PrefixesByBytes[0].Keys != 2 ||
		r.PrefixesByBytes[0].EstBytes != 400 {
		t.Errorf("PrefixesByBytes[0] = %+v", r.PrefixesByBytes[0])
	}
	if len(r.PrefixesByKeys) != 2 {
		t.Errorf("len(PrefixesByKeys) = %d, want 2", len(r.PrefixesByKeys))
	}
	if r.PrefixesByKeys[0].Prefix != "big:a" || r.PrefixesByKeys[0].Keys != 2 {
		t.Errorf("PrefixesByKeys[0] = %+v", r.PrefixesByKeys[0])
	}
}

func TestAggregatorBigKeysOrder(t *testing.T) {
	a := newAggregator(2)
	a.add(&Entry{Key: "small", Bytes: 1, Type: "string"})
	a.add(&Entry{Key: "big", Bytes: 999, Type: "hash", NumOfElem: 7})
	a.add(&Entry{Key: "mid", Bytes: 100, Type: "list"})
	r := a.finish(nil)

	if len(r.BigKeys) != 2 || r.BigKeys[0].Key != "big" || r.BigKeys[1].Key != "mid" {
		t.Errorf("BigKeys = %+v", r.BigKeys)
	}
	if r.BigKeys[0].Type != "hash" || r.BigKeys[0].NumOfElem != 7 {
		t.Errorf("BigKeys[0] = %+v", r.BigKeys[0])
	}
}

func TestAggregatorMetadataFromDecoder(t *testing.T) {
	dec := NewDecoder()
	dec.StartRDB(11)
	dec.Aux([]byte("redis-ver"), []byte("7.2.4"))
	dec.Aux([]byte("ctime"), []byte("1700000000"))
	dec.Aux([]byte("used-mem"), []byte("4096"))
	a := newAggregator(10)
	a.add(&Entry{Key: "x", Bytes: 1, Type: "string"})
	r := a.finish(dec)

	if r.RDBVersion != 11 || r.RedisVersion != "7.2.4" || r.DumpCtime != 1700000000 || r.UsedMemSnapshot != 4096 {
		t.Errorf("metadata = %+v", r)
	}
}
