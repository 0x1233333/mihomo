package smart

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"
)

type replayNode struct {
	Node   string   `json:"node"`
	Weight float64  `json:"weight"`
	Alive  *bool    `json:"alive"`
}

type replayRec struct {
	TS     string                          `json:"ts"`
	Groups map[string][]replayNode         `json:"groups"`
}

// 用真实权重快照回放: 归一化前后各组的头名是否还有"探测失败却权重最高"
func TestReplayRealSnapshots(t *testing.T) {
	path := os.Getenv("SMART_RANK_REPLAY")
	if path == "" {
		t.Skip("SMART_RANK_REPLAY not set")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var groups, topDeadBefore, topDeadAfter int
	var groupsAllAlive, groupsAllAliveChanged int
	var allDead int
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec replayRec
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		for _, nodes := range rec.Groups {
			if len(nodes) == 0 {
				continue
			}
			groups++
			names := make([]string, 0, len(nodes))
			scores := make(map[string]float64, len(nodes))
			alive := make(map[string]bool, len(nodes))
			want := make(map[string]float64, len(nodes))
			alivePositive := false
			for _, n := range nodes {
				names = append(names, n.Node)
				scores[n.Node] = n.Weight
				want[n.Node] = n.Weight
				if n.Alive != nil && *n.Alive {
					alive[n.Node] = true
					if n.Weight > 0 {
						alivePositive = true
					}
				}
			}

			best := nodes[0]
			for _, n := range nodes {
				if n.Weight > best.Weight {
					best = n
				}
			}
			bestDead := best.Alive != nil && !*best.Alive
			if bestDead {
				topDeadBefore++
			}
			if !alivePositive {
				allDead++
			}

			items := finalizeRankItems(names, scores, alive)
			if len(items) == 0 {
				continue
			}
			top := items[0]
			if !alive[top.Name] {
				topDeadAfter++
			}

			if alivePositive {
				if aliveCount(names, alive) == len(names) {
					groupsAllAlive++
					for _, it := range items {
						if it.Weight != want[it.Name] {
							groupsAllAliveChanged++
							t.Logf("changed in all-alive group: %s want %v got %v", it.Name, want[it.Name], it.Weight)
							break
						}
					}
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	t.Logf("组快照=%d 头名探测失败(回放前)=%d 头名探测失败(回放后)=%d 全死组=%d 全活组=%d 全活组被改动=%d",
		groups, topDeadBefore, topDeadAfter, allDead, groupsAllAlive, groupsAllAliveChanged)

	if topDeadBefore == 0 {
		t.Fatalf("样本里没有复现问题, 回放无意义")
	}
	// 只有当"存活节点都没有任何权重"时才会退回旧口径, 这类组的头名仍可能是死节点
	if topDeadAfter > allDead {
		t.Fatalf("回放后仍有 %d 个组的头名是死节点, 超过回退组数 %d", topDeadAfter, allDead)
	}
	if groupsAllAliveChanged != 0 {
		t.Fatalf("全活组的结果被改动了 %d 次", groupsAllAliveChanged)
	}
}

func aliveCount(names []string, alive map[string]bool) int {
	n := 0
	for _, name := range names {
		if alive[name] {
			n++
		}
	}
	return n
}
