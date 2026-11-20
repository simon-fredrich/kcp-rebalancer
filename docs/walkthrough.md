# Execution walkthrough

This project is a standalone Go CLI for rebalancing non-system logical
clusters across KCP shards. Balancing policies are selectable; the default
`simple` policy balances workspace counts, while `milp` minimizes load
imbalance while accounting for migration cost. The CLI previews plans unless
`--apply` is specified.

## 1. Process lifecycle

[The entry point](../main.go) creates a context canceled by Ctrl+C or SIGTERM,
passes command-line arguments and output streams to the CLI, and reports
failures on stderr with a nonzero exit status. Help exits successfully.

## 2. CLI configuration

[The CLI](../internal/cli/cli.go) parses flags with a fresh `flag.FlagSet` so
tests and repeated calls do not share global flag state. It validates positional
arguments, the migration timeout, `--rebalance-type`, and repeatable
`--shard-server` HTTPS endpoint overrides before opening connections. The
rebalance type names a planner in the CLI's `planners` registry. The default
policy is `simple`; preview mode remains the default.

## 3. Connect to the shards

[Discovery](../internal/kcp/discovery.go) loads the KCP kubeconfig and lists
Shards through `/clusters/root`. Each shard uses its advertised `spec.baseURL`,
unless a `--shard-server` override supplies an externally reachable endpoint.
Unknown shard overrides, duplicate or missing shard names, invalid endpoints,
and empty discovery results fail explicitly. The same kubeconfig credentials
and TLS settings are copied to each shard client.

The Go program does not launch subprocesses or manage tunnels.
[The credential script](../hack/generate-kcp-admin-kubeconfig.sh) can forward
all shards with `-p` and prints the required overrides. Keep it running while
using those endpoints.

The root configuration retains the kubeconfig endpoint with `/clusters/root`
for discovery and migration requests.

## 4. Inventory and planning

[Inventory](../internal/kcp/inventory.go) reads
`/clusters/*/apis/core.kcp.io/v1alpha1/logicalclusters` sequentially from every
shard; it is not an atomic snapshot. It excludes `root` and `system:*`
clusters, and requires ordinary clusters to have `kcp.io/cluster` and
`kcp.io/path` annotations, a `Ready` phase, and a unique identity across
shards. KCP inventory assigns every workspace a weight of one.

[The planner interface](../internal/rebalance/plan.go) separates policy from
execution. A planner receives the inventory and provides three operations:

- `Plan` proposes moves in execution order.
- `Assess` reports whether a placement satisfies the policy, with reasons if it
  does not, or an error if assessment fails.
- `PrintPlan` prints the policy-specific summary.

Planners work with Go domain types, are deterministic, and do not modify their
input. The [simple planner](../internal/rebalance/simple.go) sorts shard names
and candidate cluster identities, then simulates moves from the fullest shard
to the emptiest until counts differ by at most one. The
[MILP planner](../internal/rebalance/milp.go) minimizes total absolute
deviation of shard load from average plus `MigrationCost` per move, then
prefers fewer moves among solutions with the same objective. A zero migration
cost uses the default of one; negative or non-finite costs are rejected.
Because KCP inventory currently assigns unit weights, MILP load currently
corresponds to workspace count as well.

The MILP policy considers its placement satisfied when its optimizer proposes
no moves. This may leave unequal shard loads when the potential improvement
does not outweigh migration costs.

## 5. Preview or apply

[The runner](../internal/rebalance/runner.go) assesses the current placement,
asks the planner for moves, and rejects the plan if any move cannot be executed:
the workspace is not on the stated source, the destination is unknown or equal
to the source, or a workspace is moved twice. It then prints current and
planned counts or loads, the current assessment, and the proposed moves. In
preview mode it returns without checking or creating migration objects.

With `--apply`, it:

1. Refuses to proceed if a migration in root is not Completed or Failed.
2. Reads inventory again before each move and compares it with expected placement.
3. Creates a migration and waits for completion before submitting the next.
4. Updates expected placement after each completed move.
5. Reads final inventory, verifies that it matches the expected placement, and
   asks the planner to assess it. An unsatisfied assessment or assessment error
   fails execution.

[Migration handling](../internal/kcp/migration.go) checks for existing
migrations in root and refuses to proceed if any is neither Completed nor
Failed. It creates `LogicalClusterMigration` objects and polls immediately,
then every two seconds. API errors, a Failed phase, timeout, or cancellation
stop execution. An already submitted migration can continue independently of
this process.

## 6. Tests and future deployment

Planner tests check deterministic moves, policy satisfaction, input
preservation, and MILP load and migration-cost behavior. Runner tests use a fake
backend to cover preview output, sequential execution, placement drift, backend
errors, and final verification, and a fake planner to cover planner errors,
invalid moves, and unsatisfied final assessments. KCP tests use HTTP servers
and fake dynamic clients rather than a live cluster. CLI tests cover defaults,
overrides, help, and invalid arguments, including unknown rebalance types.

The [cluster-management scripts](../hack/) provision a local kind environment
with KCP resources and etcd-druid in namespace `kcp`. The KCP operator and
cert-manager retain their dedicated namespaces. No Prometheus stack is deployed.
All scripts share [configuration](../hack/common.sh), and
[script regression tests](../hack/tests/cluster-tools.sh) use isolated mock tools
to validate namespace scoping and lifecycle commands without a running cluster.

The backend boundary allows connection changes without changing the planner.
A Kubernetes Job can use reachable advertised shard endpoints without local
tunnels, but still needs a KCP kubeconfig with administrator credentials and
appropriate TLS trust.
