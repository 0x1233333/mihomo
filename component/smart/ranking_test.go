package smart

import (
	"math"
	"testing"
)

func findItem(items []NodeRankItem, name string) (NodeRankItem, bool) {
	for _, it := range items {
		if it.Name == name {
			return it, true
		}
	}
	return NodeRankItem{}, false
}

func almostEqual(a, b float64) bool {
	return math.Abs(a-b) < 0.011
}

// 探测失败的节点拿到的旧高分不应再被归一化成 100
func TestFinalizeRankItems_DeadTopNodeGetsZeroWeight(t *testing.T) {
	names := []string{"n1", "n2", "n3"}
	scores := map[string]float64{"n1": 2.0, "n2": 1.0, "n3": 0.5}
	alive := map[string]bool{"n1": false, "n2": true, "n3": true}

	items := finalizeRankItems(names, scores, alive)

	if len(items) != 3 {
		t.Fatalf("want 3 items, got %d", len(items))
	}
	if items[0].Name != "n2" || !almostEqual(items[0].Weight, 100) {
		t.Fatalf("alive top node should be first with weight 100, got %+v", items[0])
	}
	dead, ok := findItem(items, "n1")
	if !ok {
		t.Fatal("dead node must stay in the list")
	}
	if dead.Weight != 0 {
		t.Fatalf("dead node weight must be 0, got %v", dead.Weight)
	}
	if dead.Rank != RankRarelyUsed {
		t.Fatalf("dead node rank must be %s, got %s", RankRarelyUsed, dead.Rank)
	}
	third, _ := findItem(items, "n3")
	if !almostEqual(third.Weight, 50) {
		t.Fatalf("n3 weight should be normalized over alive nodes (50), got %v", third.Weight)
	}
}

// 全部节点探测失败时保持旧口径, 不能把排名清空
func TestFinalizeRankItems_AllDeadKeepsLegacyScores(t *testing.T) {
	names := []string{"n1", "n2"}
	scores := map[string]float64{"n1": 3.0, "n2": 1.5}
	alive := map[string]bool{"n1": false, "n2": false}

	items := finalizeRankItems(names, scores, alive)

	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	if !almostEqual(items[0].Weight, 100) || !almostEqual(items[1].Weight, 50) {
		t.Fatalf("legacy normalization must be kept when all nodes are dead, got %+v", items)
	}
	for _, it := range items {
		if it.Rank != RankRarelyUsed {
			t.Fatalf("all-dead list should be entirely %s, got %+v", RankRarelyUsed, it)
		}
	}
}

// 没有任何存活节点带分数时不做剔除(避免误清空)
func TestFinalizeRankItems_NoAliveScoredFallsBack(t *testing.T) {
	names := []string{"n1", "n2"}
	scores := map[string]float64{"n1": 2.0}
	alive := map[string]bool{"n1": false, "n2": true}

	items := finalizeRankItems(names, scores, alive)

	top, _ := findItem(items, "n1")
	if !almostEqual(top.Weight, 100) {
		t.Fatalf("fallback should keep n1 at 100, got %v", top.Weight)
	}
}

// 无死节点时结果与旧逻辑一致
func TestFinalizeRankItems_NoDeadUnchanged(t *testing.T) {
	names := []string{"n1", "n2", "n3", "n4"}
	scores := map[string]float64{"n1": 1.5, "n2": 0.5, "n3": 0.0, "n4": 1.0}
	alive := map[string]bool{"n1": true, "n2": true, "n3": true, "n4": true}

	items := finalizeRankItems(names, scores, alive)

	want := map[string]float64{"n1": 100, "n4": 66.67, "n2": 33.33, "n3": 0}
	for name, w := range want {
		it, ok := findItem(items, name)
		if !ok {
			t.Fatalf("missing %s", name)
		}
		if !almostEqual(it.Weight, w) {
			t.Fatalf("%s weight want %v got %v", name, w, it.Weight)
		}
	}
	if items[0].Name != "n1" || items[1].Name != "n4" {
		t.Fatalf("order changed: %+v", items)
	}
	if items[0].Rank != RankMostUsed {
		t.Fatalf("top alive node should be %s, got %s", RankMostUsed, items[0].Rank)
	}
}

// 空输入不应 panic
func TestFinalizeRankItems_EmptyInput(t *testing.T) {
	if items := finalizeRankItems(nil, map[string]float64{}, map[string]bool{}); items != nil {
		t.Fatalf("want nil for empty input, got %+v", items)
	}
}
