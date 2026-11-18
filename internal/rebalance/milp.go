package rebalance

import (
	"fmt"
	"io"
	"math"
	"sort"

	"github.com/bartolsthoorn/gohighs/highs"
)

type MILPPlanner struct {
	// MigrationCost trades load balance against migration count. Zero uses 1.
	MigrationCost float64
}

func (p MILPPlanner) Plan(inventory Inventory) ([]Move, error) {
	migrationCost := p.MigrationCost
	if migrationCost == 0 {
		migrationCost = 1
	}
	if migrationCost < 0 || math.IsNaN(migrationCost) || math.IsInf(migrationCost, 0) {
		return nil, fmt.Errorf("MILP migration cost must be finite and positive, got %v", migrationCost)
	}

	names := shardNames(inventory)
	numShards := len(names)
	numWorkspaces := totalWorkspaceCount(inventory)

	placements, err := getPlacements(inventory, names)
	if err != nil {
		return nil, err
	}
	if numShards == 0 || numWorkspaces == 0 {
		return nil, nil
	}

	// calculate number of variables for the first contraint
	numVars := numShards * numWorkspaces

	// initialize the model with variables for the first contraint
	model := highs.Model{
		ColCosts: make([]float64, numVars),
		ColLower: make([]float64, numVars),
		ColUpper: make([]float64, numVars),
		VarTypes: make([]highs.VariableType, numVars),
	}

	// Contraint 1: Every workspace is placed on exactly one shard
	for l := range len(placements) {
		// cols selects the summands which belong to a workspace
		cols := make([]int, numShards)
		// values contains the factor of the selected summands
		// which in this case are contantly one
		values := make([]float64, numShards)
		for s := range numShards {
			col := getIndex(numShards, s, l)
			cols[s] = col
			values[s] = 1

			// values should be either 0 or 1
			model.ColLower[col] = 0
			model.ColUpper[col] = 1
			model.VarTypes[col] = highs.Integer
			if names[s] != placements[l].shardName {
				model.ColCosts[col] = migrationCost
			}
		}

		// ensure that one workspace sits exactly on one shard
		model.AddSparseRow(1, cols, values, 1)
	}

	// Constraint 2: Deviation of shard load in regards to average load
	// should be minimized (best effort)
	totalLoad := 0
	for _, name := range names {
		totalLoad = totalLoad + shardLoad(inventory, name)
	}
	averageLoad := float64(totalLoad) / float64(numShards)

	for s := range numShards {
		// create new variable for deviation of shard s
		// cost of deviation should be 1 and deviations
		// can range between 0 and infinity
		deviationCol := len(model.ColCosts)
		model.ColCosts = append(model.ColCosts, 1)
		model.ColLower = append(model.ColLower, 0)
		model.ColUpper = append(model.ColUpper, math.Inf(1))
		model.VarTypes = append(model.VarTypes, highs.Continuous)

		// slice for saving workspace location
		// example: cols = [1 3 5]
		// 		the first two elements mark the indizes of
		// 		the workspace and the last one the index
		// 		of the deviation variable within the model
		cols := make([]int, numWorkspaces+1)
		values := make([]float64, numWorkspaces+1)
		for l, placement := range placements {
			cols[l] = getIndex(numShards, s, l)
			values[l] = float64(placement.workspace.Weight)
		}
		cols[numWorkspaces] = deviationCol

		// load = sum(workspace.Weight * assignment)

		// deviation >= load - averageLoad (Constraint part 1)
		// deviation >= averageLoad - load (Constraint part 2)

		// CP1: load - deviation <= averageLoad
		values[numWorkspaces] = -1
		model.AddSparseRow(math.Inf(-1), cols, values, averageLoad)

		// CP2: load + deviation >= averageLoad
		values[numWorkspaces] = 1
		model.AddSparseRow(averageLoad, cols, values, math.Inf(1))
	}

	solution, err := model.Solve(highs.WithOutput(false))
	if err != nil {
		return nil, fmt.Errorf("solve MILP: %w", err)
	}
	if !solution.IsOptimal() {
		return nil, fmt.Errorf("MILP did not find an optimal solution")
	}

	// Among equally good trade-offs, prefer fewer migrations.
	model.AddLeRow(model.ColCosts, solution.Objective)
	model.ColCosts = make([]float64, len(model.ColCosts))
	for l, placement := range placements {
		for s, destination := range names {
			if destination != placement.shardName {
				model.ColCosts[getIndex(numShards, s, l)] = 1
			}
		}
	}
	solution, err = model.Solve(highs.WithOutput(false))
	if err != nil {
		return nil, fmt.Errorf("minimize MILP migrations: %w", err)
	}
	if !solution.IsOptimal() {
		return nil, fmt.Errorf("MILP did not find a migration-minimal optimal solution")
	}

	// translate solution into a set of moves
	moves := make([]Move, 0)
	for l, placement := range placements {
		for s, destination := range names {
			col := getIndex(numShards, s, l)
			if solution.ColValues[col] > 0.5 {
				if destination != placement.shardName {
					moves = append(moves, Move{
						Workspace: placement.workspace,
						From:      placement.shardName,
						To:        destination,
					})
				}

				// no point in continuing for other placement
				// destination combinations if there is a
				// match already
				break
			}
		}
	}

	return moves, nil
}

func (p MILPPlanner) Assess(inventory Inventory) (Assessment, error) {
	moves, err := p.Plan(inventory)
	if err != nil {
		return Assessment{}, err
	}
	if len(moves) == 0 {
		return Assessment{Satisfied: true}, nil
	}
	return Assessment{Reasons: []string{
		"load deviation can be reduced enough to outweigh migration costs"}}, nil
}

func (MILPPlanner) PrintPlan(out io.Writer, inventory Inventory, assessment Assessment, moves []Move) error {
	return writePlanReport(out, inventory, assessment, moves, "LOAD", shardLoad)
}

type placement struct {
	workspace Workspace
	shardName string
}

func totalWorkspaceCount(inv Inventory) int {
	numLs := 0
	for _, workspaces := range inv {
		numLs = numLs + len(workspaces)
	}
	return numLs
}

func shardLoad(inv Inventory, name string) int {
	load := 0
	for _, workspace := range inv[name] {
		load = load + workspace.Weight
	}
	return load
}

func getPlacements(inv Inventory, names []string) ([]placement, error) {
	var placements []placement
	seen := map[string]bool{}
	for _, name := range names {
		workspaces := append([]Workspace(nil), inv[name]...)
		sort.Slice(workspaces, func(i, j int) bool {
			return workspaces[i].Cluster < workspaces[j].Cluster
		})
		for _, ws := range workspaces {
			if seen[ws.Cluster] {
				return nil, fmt.Errorf("cluster %s appears on multiple shards", ws.Cluster)
			}
			placements = append(placements, placement{workspace: ws, shardName: name})
			seen[ws.Cluster] = true
		}
	}
	return placements, nil
}

func getIndex(numShards, s, l int) int {
	return l*numShards + s
}
