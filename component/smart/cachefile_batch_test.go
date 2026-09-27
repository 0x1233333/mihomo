package smart

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"testing"
	"time"

	bolt "github.com/metacubex/bbolt"
)

// 这些测试针对 2026-09-27 的改造:把"整组前缀全量读进内存"的周期扫描
// 改成"逐 target 子前缀分批读"(DBListSubPrefixes + 点读)。
// 关键要求:行为与原实现等价 —— 该删的删、该留的留、返回值不变。

const (
	testConfig = "testcfg"
	testGroup  = "🇯🇵 日本"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	d, err := bolt.Open(filepath.Join(t.TempDir(), "smart_test.db"), 0600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("open bolt: %v", err)
	}
	if err := d.Update(func(tx *bolt.Tx) error {
		_, e := tx.CreateBucketIfNotExists(bucketSmartStats)
		return e
	}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	prevDB := db
	prevMax := globalCacheParams.MaxTargets
	globalCacheParams.MaxTargets = MaxTargetsLimit
	t.Cleanup(func() {
		_ = d.Close()
		db = prevDB
		globalCacheParams.MaxTargets = prevMax
	})

	// NewStore 会设置包级 db 并初始化三个内存缓存,所以先建 Store 再清缓存
	s := NewStore(d)

	// 清掉可能残留的内存缓存,避免跨测试串味
	recordCache.RemoveByKeyPrefix("smart/")
	dbResultCache.RemoveByKeyPrefix("smart/")
	hostStatusCache.RemoveByKeyPrefix("smart/")

	return s
}

func putJSON(t *testing.T, s *Store, key, js string) {
	t.Helper()
	if err := s.DBBatchPutItem(key, []byte(js)); err != nil {
		t.Fatalf("put %s: %v", key, err)
	}
}

func exists(t *testing.T, s *Store, key string) bool {
	t.Helper()
	v, err := s.DBViewGetItem(key)
	if err != nil {
		// DBViewGetItem 对不存在的 key 返回 "item not found" —— 那正是"已删除"
		return false
	}
	return len(v) > 0
}

// ---------- 1) DBListSubPrefixes:新原语本身 ----------

func TestDBListSubPrefixes(t *testing.T) {
	s := newTestStore(t)

	keys := []string{
		FormatDBKey(KeyTypeStats, testConfig, testGroup, "t1", "n1"),
		FormatDBKey(KeyTypeStats, testConfig, testGroup, "t1", "n2"),
		FormatDBKey(KeyTypeStats, testConfig, testGroup, "t10", "n1"),
		FormatDBKey(KeyTypeStats, testConfig, testGroup, "t2", "n1"),
		FormatDBKey(KeyTypeStats, testConfig, "别的组", "t1", "n1"),
		FormatDBKey(KeyTypePrefetch, testConfig, testGroup, "t1"),
	}
	for _, k := range keys {
		putJSON(t, s, k, `{"ok":1}`)
	}

	groupPrefix := FormatDBKey(KeyTypeStats, testConfig, testGroup)

	got, err := s.DBListSubPrefixes(groupPrefix, 1, true)
	if err != nil {
		t.Fatalf("depth1: %v", err)
	}
	sort.Strings(got)
	want := []string{
		groupPrefix + "/t1",
		groupPrefix + "/t10",
		groupPrefix + "/t2",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("depth1 结果不符\n got=%v\nwant=%v", got, want)
	}

	// depth=2:t1 下的两个 node
	got2, err := s.DBListSubPrefixes(groupPrefix+"/t1", 1, true)
	if err != nil {
		t.Fatalf("depth1 under t1: %v", err)
	}
	if len(got2) != 2 {
		t.Fatalf("t1 下应有 2 个 node 子前缀,得到 %v", got2)
	}

	// ⚠️ 关键回归点:统计类内所有调用点都不能把别的组的记录带进来
	all, err := s.DBListSubPrefixes(groupPrefix, 1, true)
	if err != nil {
		t.Fatalf("all: %v", err)
	}
	for _, p := range all {
		if p == "别的组" || len(p) == 0 {
			t.Fatalf("串组了: %v", all)
		}
	}
}

// ---------- 2) CleanupOldRecords:过期删除 + 超量裁剪 ----------

func TestCleanupOldRecordsExpired(t *testing.T) {
	s := newTestStore(t)

	now := time.Now().Unix()
	old := time.Now().Add(-8 * 24 * time.Hour).Unix() // > RecordExpiredTime(7d)

	mk := func(lastUsed int64, ok, fail int64) string {
		r := StatsRecord{LastUsed: lastUsed, Success: ok, Failure: fail}
		b, _ := json.Marshal(r)
		return string(b)
	}

	freshKey := FormatDBKey(KeyTypeStats, testConfig, testGroup, "t1", "fresh")
	oldKey := FormatDBKey(KeyTypeStats, testConfig, testGroup, "t1", "stale")
	oldKey2 := FormatDBKey(KeyTypeStats, testConfig, testGroup, "t2", "stale2")
	putJSON(t, s, freshKey, mk(now, 10, 1))
	putJSON(t, s, oldKey, mk(old, 5, 1))
	putJSON(t, s, oldKey2, mk(old, 5, 1))

	s.CleanupOldRecords(testGroup, testConfig)

	if !exists(t, s, freshKey) {
		t.Fatalf("最近的记录不该被删: %s", freshKey)
	}
	if exists(t, s, oldKey) || exists(t, s, oldKey2) {
		t.Fatalf("过期记录应被删除(跨多个 target 也要删干净)")
	}
}

func TestCleanupOldRecordsTrimByCount(t *testing.T) {
	s := newTestStore(t)

	// 把上限压到 1 → totalRecords(3) > maxTargets*2(2) → 走"按数量删"的分支
	globalCacheParams.MaxTargets = 1

	now := time.Now().Unix()
	mk := func(ok int64) string {
		b, _ := json.Marshal(StatsRecord{LastUsed: now, Success: ok, Failure: 0})
		return string(b)
	}
	highKey := FormatDBKey(KeyTypeStats, testConfig, testGroup, "t1", "busy")
	lowKey1 := FormatDBKey(KeyTypeStats, testConfig, testGroup, "t1", "idle1")
	lowKey2 := FormatDBKey(KeyTypeStats, testConfig, testGroup, "t2", "idle2")
	putJSON(t, s, highKey, mk(100))
	putJSON(t, s, lowKey1, mk(1))
	putJSON(t, s, lowKey2, mk(1))

	s.CleanupOldRecords(testGroup, testConfig)

	if !exists(t, s, highKey) {
		t.Fatalf("使用最多的记录必须保住")
	}
	remain := 0
	for _, k := range []string{highKey, lowKey1, lowKey2} {
		if exists(t, s, k) {
			remain++
		}
	}
	if remain > 1 {
		t.Fatalf("超量裁剪没生效:还剩 %d 条(期望 1 条)", remain)
	}
}

// ---------- 3) RemoveNodesData:只删指定节点,且四类记录都要处理 ----------

func TestRemoveNodesData(t *testing.T) {
	s := newTestStore(t)

	keepStats := FormatDBKey(KeyTypeStats, testConfig, testGroup, "t1", "n1")
	dropStats := FormatDBKey(KeyTypeStats, testConfig, testGroup, "t1", "n2")
	otherTargetStats := FormatDBKey(KeyTypeStats, testConfig, testGroup, "t2", "n2")
	putJSON(t, s, keepStats, `{"last_used":1,"success":1}`)
	putJSON(t, s, dropStats, `{"last_used":1,"success":1}`)
	putJSON(t, s, otherTargetStats, `{"last_used":1,"success":1}`)

	prefetchKey := FormatDBKey(KeyTypePrefetch, testConfig, testGroup, "t1")
	putJSON(t, s, prefetchKey, `{"updated_time":1,"tcp":{"nodes":["n1","n2"],"weights":[1,2]},"udp":{"nodes":["n2"],"weights":[3]}}`)

	rankingKey := FormatDBKey(KeyTypeRanking, testConfig, testGroup)
	putJSON(t, s, rankingKey, `{"last_updated":1,"result":[{"Name":"n1","Rank":"MostUsed","Weight":9},{"Name":"n2","Rank":"RarelyUsed","Weight":1}]}`)

	failKey := FormatDBKey(KeyTypeHostFailures, testConfig, testGroup, "t1")
	putJSON(t, s, failKey, `{"last_failure":1,"codes":{"403":{"nodes":{"n1":1,"n2":2},"fail_counts":{"n1":1,"n2":2}}}}`)

	nodeKey := FormatDBKey(KeyTypeNode, testConfig, testGroup, "n2")
	putJSON(t, s, nodeKey, `{"state":1}`)

	if err := s.RemoveNodesData(testGroup, testConfig, 3, []string{"n2"}); err != nil {
		t.Fatalf("RemoveNodesData: %v", err)
	}

	// stats:只删 n2 在 t1 下的记录
	if !exists(t, s, keepStats) {
		t.Fatalf("不该删 n1 的 stats")
	}
	if exists(t, s, dropStats) {
		t.Fatalf("应删掉 t1 下 n2 的 stats")
	}
	// ⚠️ 回归护栏:原语义是"删掉这些节点在**所有** target 下的记录"(整组按节点名过滤),
	// 不是只删某个 target。分批改造后必须保持这一点 —— 想"顺手"改成只删当前 target 会破坏它。
	if exists(t, s, otherTargetStats) {
		t.Fatalf("按节点名清理必须覆盖整组所有 target,漏了 %s", otherTargetStats)
	}

	// prefetch:值里去掉 n2,保留 n1
	raw, _ := s.DBViewGetItem(prefetchKey) // 不存在时返回 err,raw 为空,下面的 len 判断已覆盖
	var pm struct {
		TCP struct {
			Nodes   []string  `json:"nodes"`
			Weights []float64 `json:"weights"`
		} `json:"tcp"`
		UDP struct {
			Nodes []string `json:"nodes"`
		} `json:"udp"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &pm); err != nil {
			t.Fatalf("prefetch 反序列化失败: %v", err)
		}
		if fmt.Sprint(pm.TCP.Nodes) != "[n1]" {
			t.Fatalf("prefetch TCP 应只剩 n1,实得 %v", pm.TCP.Nodes)
		}
		if len(pm.UDP.Nodes) != 0 {
			t.Fatalf("prefetch UDP 应清空,实得 %v", pm.UDP.Nodes)
		}
	}

	// ranking:去掉 n2,保留 n1
	rawR, _ := s.DBViewGetItem(rankingKey)
	var nr struct {
		Result []struct{ Name string }
	}
	if len(rawR) > 0 {
		if err := json.Unmarshal(rawR, &nr); err != nil {
			t.Fatalf("ranking 反序列化失败: %v", err)
		}
		if len(nr.Result) != 1 || nr.Result[0].Name != "n1" {
			t.Fatalf("ranking 应只剩 n1,实得 %+v", nr.Result)
		}
	}

	// failures:codeSet 里的 n2 要清掉
	rawF, _ := s.DBViewGetItem(failKey)
	if len(rawF) > 0 {
		var hs struct {
			Codes map[string]struct {
				Nodes      map[string]int64
				FailCounts map[string]int
			}
		}
		if err := json.Unmarshal(rawF, &hs); err != nil {
			t.Fatalf("failures 反序列化失败: %v", err)
		}
		for _, cs := range hs.Codes {
			if _, bad := cs.Nodes["n2"]; bad {
				t.Fatalf("failures 里 n2 没清干净")
			}
			if _, bad := cs.FailCounts["n2"]; bad {
				t.Fatalf("failures 的 fail_counts 里 n2 没清干净")
			}
		}
	}

	// node 状态:按节点直删
	if exists(t, s, nodeKey) {
		t.Fatalf("节点状态记录应被删掉")
	}
}

// ---------- 4) GetAllGroupsForConfig:仍能列出所有组 ----------

func TestGetAllGroupsForConfig(t *testing.T) {
	s := newTestStore(t)

	for _, g := range []string{"🇯🇵 日本", "🇰🇷 韩国", "🌍 其他地区"} {
		putJSON(t, s, FormatDBKey(KeyTypeStats, testConfig, g, "t1", "n1"), `{"last_used":1,"success":1}`)
	}
	// 另一个 config 的记录不应混进来
	putJSON(t, s, FormatDBKey(KeyTypeStats, "othercfg", "别的配置组", "t1", "n1"), `{"last_used":1,"success":1}`)

	groups, err := s.GetAllGroupsForConfig(testConfig)
	if err != nil {
		t.Fatalf("GetAllGroupsForConfig: %v", err)
	}
	found := map[string]bool{}
	for _, g := range groups {
		found[g] = true
	}
	for _, want := range []string{"🇯🇵 日本", "🇰🇷 韩国", "🌍 其他地区"} {
		if !found[want] {
			t.Fatalf("漏了组 %q(实得 %v)", want, groups)
		}
	}
	if found["别的配置组"] {
		t.Fatalf("串到别的 config 去了: %v", groups)
	}
}
