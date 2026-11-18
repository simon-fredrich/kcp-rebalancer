package rebalance

import "io"

// Planner is a balancing policy. Implementations must be deterministic, must
// not modify the snapshot; the runner validates and executes the moves they
// propose.
type Planner interface {
	// Plan returns the moves, in execution order, that this policy proposes.
	Plan(Inventory) ([]Move, error)
	// Assess reports whether a placement satisfies this policy. It returns an
	// error, not an unsatisfied assessment, when it cannot judge the snapshot,
	// for example because required observations are missing or stale.
	Assess(Inventory) (Assessment, error)
	// PrintPlan writes a planner-specific summary of the proposed moves.
	PrintPlan(io.Writer, Inventory, Assessment, []Move) error
}

type Assessment struct {
	Satisfied bool
	// Reasons explains why the placement does not satisfy the policy.
	Reasons []string
}
