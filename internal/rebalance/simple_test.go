package rebalance

import (
	"fmt"
	"reflect"
	"testing"
)

func TestSimplePlannerPlan(t *testing.T) {
	tests := []struct {
		counts []int
		moves  int
	}{
		{[]int{}, 0},
		{[]int{9}, 0},
		{[]int{0, 0, 0}, 0},
		{[]int{3, 3, 3}, 0},
		{[]int{2, 3, 3}, 0},
		{[]int{5, 1, 3}, 2},
		{[]int{9, 0, 0}, 6},
		{[]int{1, 0, 0}, 0},
		{[]int{4, 4, 0, 0}, 4},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.counts), func(t *testing.T) {
			input := Inventory{}
			for shard, count := range tt.counts {
				name := fmt.Sprintf("shard-%d", shard)
				input[name] = []Workspace{}
				for i := 0; i < count; i++ {
					id := fmt.Sprintf("%s-cluster-%d", name, i)
					input[name] = append(input[name], Workspace{Cluster: id, Path: "root:" + id})
				}
			}
			before := cloneInventory(input)
			moves, err := SimplePlanner{}.Plan(input)
			if err != nil {
				t.Fatal(err)
			}
			if len(moves) != tt.moves {
				t.Fatalf("got %d moves, want %d", len(moves), tt.moves)
			}
			again, _ := SimplePlanner{}.Plan(input)
			if !reflect.DeepEqual(moves, again) {
				t.Fatal("plan is not deterministic")
			}
			counts := map[string]int{}
			seen := map[string]bool{}
			for name, items := range input {
				counts[name] = len(items)
			}
			for _, m := range moves {
				if seen[m.Workspace.Cluster] {
					t.Fatal("workspace moved twice")
				}
				seen[m.Workspace.Cluster] = true
				found := false
				for _, w := range input[m.From] {
					if w == m.Workspace {
						found = true
					}
				}
				if !found {
					t.Fatalf("move has incorrect source: %+v", m)
				}
				counts[m.From]--
				counts[m.To]++
			}
			for _, a := range counts {
				for _, b := range counts {
					if a-b > 1 {
						t.Fatalf("not balanced: %v", counts)
					}
				}
			}
			if !reflect.DeepEqual(before, input) {
				t.Fatal("input modified")
			}
		})
	}
}

func TestSimplePlannerAssess(t *testing.T) {
	for _, tt := range []struct {
		name      string
		input     Inventory
		satisfied bool
	}{
		{"no-shards", Inventory{}, true},
		{"balanced", Inventory{"a": {{"one", "root:one", 1}}, "b": {}}, true},
		{"unbalanced", Inventory{"a": {{"one", "root:one", 1}, {"two", "root:two", 1}}, "b": {}}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SimplePlanner{}.Assess(tt.input)
			if err != nil {
				t.Fatal(err)
			}
			if got.Satisfied != tt.satisfied || got.Satisfied != (len(got.Reasons) == 0) {
				t.Fatalf("unexpected assessment: %+v", got)
			}
		})
	}
}
