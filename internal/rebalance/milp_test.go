package rebalance

import (
	"bytes"
	"math"
	"strings"
	"testing"
)

func TestMILPPlanMigrationCost(t *testing.T) {
	for _, tt := range []struct {
		name          string
		inventory     Inventory
		migrationCost float64
		wantMoves     int
		wantDeviation float64
	}{
		{
			name: "one move balances two zero two",
			inventory: Inventory{
				"a": {{"one", "user:admin:one", 1}, {"two", "user:admin:two", 1}},
				"b": {},
				"c": {{"three", "user:admin:three", 1}, {"four", "user:admin:four", 1}},
			},
			wantMoves: 1, wantDeviation: 4.0 / 3,
		},
		{
			name: "balanced placement stays put",
			inventory: Inventory{
				"a": {{"one", "root:one", 1}},
				"b": {{"two", "root:two", 1}},
			},
		},
		{
			name: "weights determine benefit",
			inventory: Inventory{
				"a": {{"one", "root:one", 2}, {"two", "root:two", 2}},
				"b": {},
			},
			migrationCost: 3, wantMoves: 1,
		},
		{
			name: "expensive migration tolerates imbalance",
			inventory: Inventory{
				"a": {{"one", "root:one", 1}, {"two", "root:two", 1}},
				"b": {},
			},
			migrationCost: 3, wantMoves: 0, wantDeviation: 2,
		},
		{
			name: "equal benefit and cost prefers no move",
			inventory: Inventory{
				"a": {{"one", "root:one", 1}, {"two", "root:two", 1}},
				"b": {},
			},
			migrationCost: 2, wantMoves: 0, wantDeviation: 2,
		},
		{name: "no shards", inventory: Inventory{}},
		{name: "empty shards", inventory: Inventory{"a": {}, "b": {}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			planner := MILPPlanner{MigrationCost: tt.migrationCost}
			before := cloneInventory(tt.inventory)
			moves, err := planner.Plan(tt.inventory)
			if err != nil {
				t.Fatal(err)
			}
			if len(moves) != tt.wantMoves {
				t.Fatalf("got %d moves, want %d: %v", len(moves), tt.wantMoves, moves)
			}
			if !sameInventory(before, tt.inventory) {
				t.Fatal("planning modified inventory")
			}
			if err := validateMoves(tt.inventory, moves); err != nil {
				t.Fatal(err)
			}
			current, err := planner.Assess(tt.inventory)
			if err != nil {
				t.Fatal(err)
			}
			if current.Satisfied != (tt.wantMoves == 0) || (!current.Satisfied && len(current.Reasons) == 0) {
				t.Fatalf("assessment inconsistent with plan: %+v", current)
			}
			for _, move := range moves {
				applyMove(tt.inventory, move)
			}
			totalLoad := 0
			for name := range tt.inventory {
				totalLoad += shardLoad(tt.inventory, name)
			}
			deviation := 0.0
			for name := range tt.inventory {
				deviation += math.Abs(float64(shardLoad(tt.inventory, name)) -
					float64(totalLoad)/float64(len(tt.inventory)))
			}
			if math.Abs(deviation-tt.wantDeviation) > 1e-8 {
				t.Errorf("planned load deviation = %v, want %v", deviation, tt.wantDeviation)
			}
			final, err := planner.Assess(tt.inventory)
			if err != nil || !final.Satisfied {
				t.Fatalf("final placement must satisfy the policy: %+v, %v", final, err)
			}
		})
	}
}

func TestMILPPlanRejectsInvalidInput(t *testing.T) {
	for _, cost := range []float64{-1, math.NaN(), math.Inf(1)} {
		if _, err := (MILPPlanner{MigrationCost: cost}).Plan(Inventory{}); err == nil {
			t.Errorf("expected error for migration cost %v", cost)
		}
	}
	inventory := Inventory{
		"a": {{"duplicate", "root:team", 1}},
		"b": {{"duplicate", "root:team", 1}},
	}
	if _, err := (MILPPlanner{}).Plan(inventory); err == nil {
		t.Fatal("expected duplicate placement error")
	}
}

func TestMILPPrintPlanShowsLoadsBeforeAndAfterMoves(t *testing.T) {
	moved := Workspace{Cluster: "two", Path: "root:two", Weight: 3}
	inventory := Inventory{
		"a": {
			{Cluster: "one", Path: "root:one", Weight: 2},
			moved,
		},
		"b": {{Cluster: "three", Path: "root:three", Weight: 4}},
	}
	moves := []Move{{Workspace: moved, From: "a", To: "b"}}
	var out bytes.Buffer

	if err := (MILPPlanner{}).PrintPlan(&out, inventory, Assessment{Satisfied: true}, moves); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(out.String(), "\n")
	for i, want := range []string{
		"SHARD CURRENT LOAD PLANNED LOAD",
		"a 5 2",
		"b 4 7",
	} {
		if got := strings.Join(strings.Fields(lines[i]), " "); got != want {
			t.Errorf("line %d = %q, want %q", i+1, got, want)
		}
	}
}
