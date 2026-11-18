package rebalance

import (
	"fmt"
	"io"
	"sort"
)

// SimplePlanner balances workspace counts: shards may differ by at most one
// workspace. It ignores workspace size and load.
type SimplePlanner struct{}

// Plan moves workspaces from the fullest to the emptiest shard until counts
// differ by at most one.
func (SimplePlanner) Plan(inventory Inventory) ([]Move, error) {
	names := shardNames(inventory)
	counts := map[string]int{}
	candidates := map[string][]Workspace{}
	for name, workspaces := range inventory {
		counts[name] = len(workspaces)
		candidates[name] = append([]Workspace(nil), workspaces...)
		sort.Slice(candidates[name], func(i, j int) bool {
			return candidates[name][i].Cluster < candidates[name][j].Cluster
		})
	}
	var moves []Move
	if len(names) == 0 {
		return moves, nil
	}
	for {
		from, to := fullestAndEmptiest(names, counts)
		if counts[from]-counts[to] <= 1 {
			return moves, nil
		}
		w := candidates[from][0]
		candidates[from] = candidates[from][1:]
		moves = append(moves, Move{Workspace: w, From: from, To: to})
		counts[from]--
		counts[to]++
	}
}

func (SimplePlanner) Assess(inventory Inventory) (Assessment, error) {
	names := shardNames(inventory)
	if len(names) == 0 {
		return Assessment{Satisfied: true}, nil
	}
	counts := map[string]int{}
	for name, workspaces := range inventory {
		counts[name] = len(workspaces)
	}
	from, to := fullestAndEmptiest(names, counts)
	if counts[from]-counts[to] <= 1 {
		return Assessment{Satisfied: true}, nil
	}
	return Assessment{Reasons: []string{fmt.Sprintf(
		"shard %s has %d workspaces and shard %s has %d; counts may differ by at most one",
		from, counts[from], to, counts[to])}}, nil
}

func (SimplePlanner) PrintPlan(out io.Writer, inventory Inventory, assessment Assessment, moves []Move) error {
	return writePlanReport(out, inventory, assessment, moves, "WORKSPACES",
		func(inv Inventory, name string) int { return len(inv[name]) })
}

// fullestAndEmptiest picks the first fullest and first emptiest shard in
// names order, so ties resolve deterministically.
func fullestAndEmptiest(names []string, counts map[string]int) (from, to string) {
	from, to = names[0], names[0]
	for _, name := range names {
		if counts[name] > counts[from] {
			from = name
		}
		if counts[name] < counts[to] {
			to = name
		}
	}
	return from, to
}
