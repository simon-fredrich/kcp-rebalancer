package rebalance

import (
	"sort"
)

type Workspace struct {
	Cluster string
	Path    string
	Weight  int
}

type Move struct {
	Workspace Workspace
	From, To  string
}

// Inventory maps shard names to the workspaces they host.
type Inventory map[string][]Workspace

type SubjectKind string

const (
	ShardSubject     SubjectKind = "shard"
	WorkspaceSubject SubjectKind = "workspace"
)

// supply shard names from inventory
func shardNames(inventory Inventory) []string {
	names := make([]string, 0, len(inventory))
	for name := range inventory {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
