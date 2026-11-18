package rebalance

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type fakeBackend struct {
	inventory     Inventory
	reads, checks int
	moves         []Move
	readErr       error
	checkErr      error
	moveErr       error
	changeOnRead  int
	skipMove      bool
}

func (b *fakeBackend) Inventory(context.Context) (Inventory, error) {
	b.reads++
	if b.readErr != nil {
		return nil, b.readErr
	}
	result := cloneInventory(b.inventory)
	if b.reads == b.changeOnRead {
		result["a"] = append(result["a"], Workspace{Cluster: "unexpected", Path: "root:unexpected"})
	}
	return result, nil
}

func (b *fakeBackend) CheckMigrations(context.Context) error {
	b.checks++
	return b.checkErr
}

func (b *fakeBackend) Migrate(_ context.Context, m Move, timeout time.Duration) error {
	b.moves = append(b.moves, m)
	if timeout != time.Minute {
		return errors.New("incorrect migration timeout")
	}
	if b.moveErr != nil {
		return b.moveErr
	}
	if !b.skipMove {
		for i, w := range b.inventory[m.From] {
			if w == m.Workspace {
				b.inventory[m.From] = append(b.inventory[m.From][:i], b.inventory[m.From][i+1:]...)
				break
			}
		}
		b.inventory[m.To] = append(b.inventory[m.To], m.Workspace)
	}
	return nil
}

func newBackend() *fakeBackend {
	return &fakeBackend{inventory: Inventory{
		"a": {{"three", "root:three", 1}, {"one", "root:one", 1}, {"two", "root:two", 1}},
		"b": {},
		"c": {},
	}}
}

// fakePlanner returns fixed results so runner behavior can be tested
// independently of a real policy.
type fakePlanner struct {
	moves              []Move
	planErr, assessErr error
	assessment         Assessment
}

func (p fakePlanner) Plan(Inventory) ([]Move, error) { return p.moves, p.planErr }

func (p fakePlanner) Assess(Inventory) (Assessment, error) { return p.assessment, p.assessErr }

func (fakePlanner) PrintPlan(io.Writer, Inventory, Assessment, []Move) error { return nil }

func simpleOptions(apply bool) Options {
	return Options{Planner: SimplePlanner{}, Apply: apply, MigrationTimeout: time.Minute}
}

func TestRunDryRun(t *testing.T) {
	backend := newBackend()
	var out bytes.Buffer
	if err := Run(context.Background(), backend, &out, simpleOptions(false)); err != nil {
		t.Fatal(err)
	}
	want := "SHARD  CURRENT WORKSPACES  PLANNED WORKSPACES\n" +
		"a      3                   1\nb      0                   1\nc      0                   1\n" +
		"Current placement does not satisfy the policy: shard a has 3 workspaces and shard b has 0; counts may differ by at most one\n" +
		"MOVE root:one (one): a -> b\nMOVE root:three (three): a -> c\n" +
		"Dry-run: 2 migrations planned; no resources changed. Use --apply to execute.\n"
	if out.String() != want {
		t.Fatalf("unexpected output:\n%s\nwant:\n%s", out.String(), want)
	}
	if backend.reads != 1 || backend.checks != 0 || len(backend.moves) != 0 {
		t.Fatalf("dry-run performed writes or extra checks: %+v", backend)
	}
}

func TestRunApply(t *testing.T) {
	backend := newBackend()
	var out bytes.Buffer
	if err := Run(context.Background(), backend, &out, simpleOptions(true)); err != nil {
		t.Fatal(err)
	}
	if backend.reads != 4 || backend.checks != 1 || len(backend.moves) != 2 {
		t.Fatalf("incorrect execution sequence: %+v", backend)
	}
	assessment, _ := SimplePlanner{}.Assess(backend.inventory)
	if !assessment.Satisfied || !strings.Contains(out.String(), "Verified:") {
		t.Fatalf("not verified: %s", out.String())
	}
}

func TestRunStopsOnErrors(t *testing.T) {
	sentinel := errors.New("backend unavailable")
	for _, tt := range []struct {
		name      string
		configure func(*fakeBackend)
		want      string
		moves     int
	}{
		{"inventory", func(b *fakeBackend) { b.readErr = sentinel }, sentinel.Error(), 0},
		{"active-migration", func(b *fakeBackend) { b.checkErr = sentinel }, sentinel.Error(), 0},
		{"placement-drift", func(b *fakeBackend) { b.changeOnRead = 2 }, "placement changed", 0},
		{"migration", func(b *fakeBackend) { b.moveErr = sentinel }, sentinel.Error(), 1},
		{"final-verification", func(b *fakeBackend) { b.changeOnRead = 4 }, "final placement", 2},
		{"move-not-applied", func(b *fakeBackend) { b.skipMove = true }, "placement changed", 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			backend := newBackend()
			tt.configure(backend)
			var out bytes.Buffer
			err := Run(context.Background(), backend, &out, simpleOptions(true))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected %q, got %v", tt.want, err)
			}
			if len(backend.moves) != tt.moves || strings.Contains(out.String(), "Verified:") {
				t.Fatalf("continued after error: %+v; output: %s", backend, out.String())
			}
		})
	}
}

func TestRunRejectsPlannerResults(t *testing.T) {
	sentinel := errors.New("planner failed")
	one := Workspace{"one", "root:one", 1}
	for _, tt := range []struct {
		name    string
		planner fakePlanner
		want    string
	}{
		{"plan-error", fakePlanner{planErr: sentinel}, sentinel.Error()},
		{"assess-error", fakePlanner{assessErr: sentinel}, sentinel.Error()},
		{"wrong-source", fakePlanner{moves: []Move{{one, "b", "c"}}}, "not on source shard"},
		{"unknown-workspace", fakePlanner{moves: []Move{{Workspace{"x", "root:x", 1}, "a", "b"}}}, "not on source shard"},
		{"unknown-destination", fakePlanner{moves: []Move{{one, "a", "missing"}}}, "invalid destination"},
		{"same-shard", fakePlanner{moves: []Move{{one, "a", "a"}}}, "invalid destination"},
		{"moved-twice", fakePlanner{moves: []Move{{one, "a", "b"}, {one, "b", "c"}}}, "more than once"},
		{"final-unsatisfied", fakePlanner{assessment: Assessment{Reasons: []string{"too hot"}}}, "too hot"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			backend := newBackend()
			var out bytes.Buffer
			opts := Options{Planner: tt.planner, Apply: true, MigrationTimeout: time.Minute}
			err := Run(context.Background(), backend, &out, opts)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected %q, got %v", tt.want, err)
			}
			if len(backend.moves) != 0 || strings.Contains(out.String(), "Verified:") {
				t.Fatalf("continued after error: %+v; output: %s", backend, out.String())
			}
		})
	}
}

func TestSameInventory(t *testing.T) {
	a := Inventory{"a": {{"one", "root:one", 1}, {"two", "root:two", 1}}, "b": {}}
	b := Inventory{"a": {{"two", "root:two", 1}, {"one", "root:one", 1}}, "b": {}}
	if !sameInventory(a, b) {
		t.Fatal("ordering should not affect comparison")
	}
	b["a"] = b["a"][:1]
	b["b"] = []Workspace{{"one", "root:one", 1}}
	if sameInventory(a, b) {
		t.Fatal("placement change was not detected")
	}
}
