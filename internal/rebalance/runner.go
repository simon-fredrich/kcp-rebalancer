package rebalance

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

type Backend interface {
	Inventory(context.Context) (Inventory, error)
	CheckMigrations(context.Context) error
	Migrate(context.Context, Move, time.Duration) error
}

type Options struct {
	Planner Planner
	// Apply executes the plan; otherwise Run only prints it.
	Apply bool
	// MigrationTimeout bounds the wait for each migration.
	MigrationTimeout time.Duration
}

func Run(ctx context.Context, backend Backend, out io.Writer, opts Options) error {
	inventory, err := backend.Inventory(ctx)
	if err != nil {
		return err
	}

	assessment, err := opts.Planner.Assess(inventory)
	if err != nil {
		return fmt.Errorf("assess current placement: %w", err)
	}
	moves, err := opts.Planner.Plan(inventory)
	if err != nil {
		return fmt.Errorf("plan moves: %w", err)
	}
	if err := validateMoves(inventory, moves); err != nil {
		return fmt.Errorf("planner proposed an invalid move: %w", err)
	}
	if err := opts.Planner.PrintPlan(out, inventory, assessment, moves); err != nil {
		return fmt.Errorf("print plan: %w", err)
	}
	if !opts.Apply {
		fmt.Fprintf(out, "Dry-run: %d migrations planned; no resources changed. Use --apply to execute.\n", len(moves))
		return nil
	}
	if err := backend.CheckMigrations(ctx); err != nil {
		return err
	}
	for _, m := range moves {
		current, err := backend.Inventory(ctx)
		if err != nil {
			return err
		}
		if !sameInventory(inventory, current) {
			return fmt.Errorf("placement changed since planning; rerun to compute a new plan")
		}
		if err := backend.Migrate(ctx, m, opts.MigrationTimeout); err != nil {
			return err
		}
		applyMove(inventory, m)
	}
	final, err := backend.Inventory(ctx)
	if err != nil {
		return err
	}
	if !sameInventory(inventory, final) {
		return fmt.Errorf("final placement does not match the plan; inspect the cluster")
	}
	assessment, err = opts.Planner.Assess(final)
	if err != nil {
		return fmt.Errorf("assess final placement: %w", err)
	}
	if !assessment.Satisfied {
		return fmt.Errorf("final placement does not satisfy the policy: %s; inspect the cluster",
			strings.Join(assessment.Reasons, "; "))
	}
	fmt.Fprintln(out, "Verified: final placement satisfies the policy.")
	return nil
}

func writePlanReport(
	out io.Writer,
	inventory Inventory,
	assessment Assessment,
	moves []Move,
	quantity string,
	value func(Inventory, string) int,
) error {
	planned := cloneInventory(inventory)
	for _, m := range moves {
		applyMove(planned, m)
	}
	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(table, "SHARD\tCURRENT %s\tPLANNED %s\n", quantity, quantity)
	for _, name := range shardNames(inventory) {
		fmt.Fprintf(table, "%s\t%d\t%d\n", name, value(inventory, name), value(planned, name))
	}
	if err := table.Flush(); err != nil {
		return err
	}
	if assessment.Satisfied {
		if _, err := fmt.Fprintln(out, "Current placement satisfies the policy."); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintf(out, "Current placement does not satisfy the policy: %s\n",
			strings.Join(assessment.Reasons, "; ")); err != nil {
			return err
		}
	}
	for _, m := range moves {
		if _, err := fmt.Fprintf(out, "MOVE %s (%s): %s -> %s\n",
			m.Workspace.Path, m.Workspace.Cluster, m.From, m.To); err != nil {
			return err
		}
	}
	return nil
}

// validateMoves rejects moves that cannot be executed in order against
// inventory, regardless of which planner proposed them.
func validateMoves(inventory Inventory, moves []Move) error {
	moved := map[string]bool{}
	for _, m := range moves {
		if moved[m.Workspace.Cluster] {
			return fmt.Errorf("%s is moved more than once", m.Workspace.Path)
		}
		moved[m.Workspace.Cluster] = true
		if !hosts(inventory[m.From], m.Workspace) {
			return fmt.Errorf("%s is not on source shard %q", m.Workspace.Path, m.From)
		}
		if _, exists := inventory[m.To]; !exists || m.To == m.From {
			return fmt.Errorf("%s has invalid destination shard %q", m.Workspace.Path, m.To)
		}
	}
	return nil
}

func hosts(workspaces []Workspace, w Workspace) bool {
	for _, candidate := range workspaces {
		if candidate == w {
			return true
		}
	}
	return false
}

// applyMove updates inventory in place as if m had completed.
func applyMove(inventory Inventory, m Move) {
	for i, w := range inventory[m.From] {
		if w.Cluster == m.Workspace.Cluster {
			inventory[m.From] = append(inventory[m.From][:i], inventory[m.From][i+1:]...)
			break
		}
	}
	inventory[m.To] = append(inventory[m.To], m.Workspace)
}

func cloneInventory(in Inventory) Inventory {
	out := make(Inventory, len(in))
	for name, items := range in {
		out[name] = append([]Workspace{}, items...)
	}
	return out
}

// sameInventory compares placement and workspace values, ignoring order.
func sameInventory(a, b Inventory) bool {
	if len(a) != len(b) {
		return false
	}
	for name, items := range a {
		other, exists := b[name]
		if !exists || len(items) != len(other) {
			return false
		}
		clusters := map[Workspace]bool{}
		for _, w := range items {
			clusters[w] = true
		}
		for _, w := range other {
			if !clusters[w] {
				return false
			}
		}
	}
	return true
}
