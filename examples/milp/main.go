package main

import (
	"fmt"
	"log"
	"math"
	"sort"

	"github.com/bartolsthoorn/gohighs/highs"
)

type Workspace struct {
	Cluster string
	Path    string
	Weight  int
}

type Inventory map[string][]Workspace

type placement struct {
	workspace Workspace
	shardName string
}

func shardNames(inventory Inventory) []string {
	names := make([]string, 0, len(inventory))
	for name := range inventory {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
		for _, ws := range inv[name] {
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

func main() {
	inv := Inventory{
		"shard-1": {
			{"cluster-1", "path-1", 1},
			{"cluster-2", "path-2", 1},
			{"cluster-3", "path-3", 1},
			{"cluster-4", "path-4", 1},
			{"cluster-5", "path-5", 1},
		},
		"shard-2": {
			{"cluster-6", "path-6", 1},
			{"cluster-7", "path-7", 1},
			{"cluster-8", "path-8", 10},
		},
		"shard-3": {
			{"cluster-9", "path-9", 1},
		},
	}

	names := shardNames(inv)
	numShards := len(names)
	numWorkspaces := totalWorkspaceCount(inv)

	// calculate number of variables for the first contraint
	numVars := numShards * numWorkspaces

	// initialize the model with variables for the first contraint
	model := highs.Model{
		ColCosts: make([]float64, numVars),
		ColLower: make([]float64, numVars),
		ColUpper: make([]float64, numVars),
		VarTypes: make([]highs.VariableType, numVars),
	}

	placements, err := getPlacements(inv, names)
	if err != nil {
		log.Fatal(err)
	}

	// Contraint 1: Every workspace is placed on exactly one shard
	for l, placement := range placements {
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
		}
		fmt.Printf("These are the cols from Workspace %s: %d\n", placement.workspace.Cluster, cols)

		// ensure that one workspace sits exactly on one shard
		model.AddSparseRow(1, cols, values, 1)
	}

	// Constraint 2: Deviation of shard load in regards to average load
	// should be minimized (best effort)
	totalLoad := 0
	for _, name := range names {
		totalLoad = totalLoad + shardLoad(inv, name)
	}
	averageLoad := float64(totalLoad) / float64(numShards)
	fmt.Printf("The average load is %f\n", averageLoad)

	for s, name := range names {
		fmt.Printf("Adding constraint for shard %s (index %d)\n", name, s)
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
		fmt.Print("Contraint (load - deviation <= averageLoad):\n")
		fmt.Print("-infinity <= ")
		for i := range numWorkspaces {
			fmt.Printf("%d*%.2f + ", cols[i], values[i])
		}
		fmt.Printf(" <= %f\n", averageLoad)

		// CP2: load + deviation >= averageLoad
		values[numWorkspaces] = 1
		model.AddSparseRow(averageLoad, cols, values, math.Inf(1))
		fmt.Print("Contraint (load + deviation >= averageLoad):\n")
		fmt.Printf("%f <= ", averageLoad)
		for i := range numWorkspaces {
			fmt.Printf("%d*%.2f", cols[i], values[i])
		}
		fmt.Print(" <= infinity\n\n")
	}

	solution, err := model.Solve(highs.WithOutput(false))
	if err != nil {
		log.Fatal(err)
	}

	if solution.IsOptimal() {
		fmt.Printf("Solution = %.2f\n", solution.ColValues)
		fmt.Printf("Objective = %.2f\n", solution.Objective)
	}

}
