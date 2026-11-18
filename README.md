# KCP workspace rebalancer

A standalone Go CLI that rebalances non-system logical clusters across
operator-managed KCP shards. The balancing policy is selected with
`--rebalance-type`; the default `simple` policy balances workspace counts.
It previews a deterministic plan by default and executes migrations only with
`--apply`.

## Requirements

- Go 1.26.0 or newer.
- `kubectl` on `PATH` for cluster-management scripts; the Go CLI does not open port-forwards.
- For local cluster management: `kind`, Podman (or Docker), `helm`, and `git`.
- For administrator credential generation: `jq`, `openssl`, and `base64`.
- For workspace creation with `make create-workspaces`: the `kubectl ws` plugin.
- KCP administrator credentials trusted by every shard, with access to list
  Shards in `root` and reachable shard endpoints.
- `LogicalClusterMigration=true` enabled on all shards and the cache.
- Migration API bound in `root`, as described in the
  [KCP migration guide](https://docs.kcp.io/kcp/v0.33/concepts/sharding/migration/).

The program discovers Shards through KCP's root API and uses their `spec.baseURL`
endpoints. For externally forwarded endpoints, pass repeatable
`--shard-server=shard-name=https://host:port` overrides.
Inventory includes Ready workspaces with a `kcp.io/path` annotation, including
both root paths such as `root:org-1` and user-home paths such as `user:kcp-admin:org-1`.
The `root` and `system:*` logical clusters are excluded.

## Run

From this project directory:

```sh
go run . --help
go run .
```

Generate local cluster credentials using the cluster-management workflow below.
No kubeconfigs, certificates, or keys are copied from the original project.

The default KCP kubeconfig is `/tmp/kcp-admin.kubeconfig`; override it with
`--kcp-kubeconfig`. The Go CLI needs no hosting-cluster kubeconfig or namespace.
It preserves the kubeconfig's credentials and TLS settings for shard requests.

To apply a plan, add `--apply`:

```sh
go run . --apply
```

### Balancing policies

`--rebalance-type` selects the policy used to plan moves and to verify the
result. Available policies:

| Type | Policy |
| --- | --- |
| `simple` (default) | Shard workspace counts differ by at most one. Workspace size and load are ignored. |
| `milp` | Minimize total absolute deviation from average shard load plus migration cost; prefer fewer migrations when objective values tie. |

The MILP objective is `sum(abs(shard load - average load)) + MigrationCost * moves`.
`MILPPlanner.MigrationCost` defaults to `1` when unset (zero); set a positive,
finite value when constructing the planner to change the trade-off. Higher costs
tolerate more imbalance to avoid migrations. There is no CLI flag for this yet.
Assessment is satisfied when no move improves this objective; it does not
necessarily mean all shard loads are equal.

Each policy implements the `Planner` interface in
[internal/rebalance/plan.go](internal/rebalance/plan.go) and is registered in
[internal/cli/cli.go](internal/cli/cli.go).

## Manage the local cluster

```sh
make up                                   # Create kind, install cert-manager, then deploy KCP
export KUBECONFIG=.kube/kind.kubeconfig   # Show hosting contexts and the shell export command
export KUBECONFIG=.kube/admin.kubeconfig  # Show KCP admin contexts and the shell export command
make add-shard ARGS=shard-03
make create-workspaces                    # Create team-1 through team-100 (default; requires kubectl ws)
make create-workspaces ARGS=10            # Create team-1 through team-10
```

KCP's RootShard, Shards, front proxy, certificates, kubeconfig resources, and
etcd instances live in namespace **`kcp`**. The etcd-druid Helm release also
lives there. The KCP operator lives in **`kcp-operator`**, and cert-manager
uses **`cert-manager`**. Prometheus is not installed.

The front proxy is exposed at `https://localhost:8443` through kind's NodePort
mapping.

`make up` creates `.kube/kind.kubeconfig` and `.kube/admin.kubeconfig`.
The latter is for ordinary KCP administration through the front proxy, not the
unrestricted cross-shard inventory used by the rebalancer.

To generate the rebalancer's separate KCP credentials and forward all shard
endpoints, keep this running in another terminal with the hosting Kubernetes
context selected:

```sh
KUBECONFIG=.kube/kind.kubeconfig hack/generate-kcp-admin-kubeconfig.sh -p
```

The script writes `/tmp/kcp-admin.kubeconfig` and prints `--shard-server`
arguments. Pass all printed arguments to the rebalancer, for example:

```sh
make enable-migrations     # Enables migration gates and binds migration.kcp.io
go run . --shard-server=root=https://127.0.0.1:8443 \
  --shard-server=shard-01=https://127.0.0.1:8444
```

Use the actual shard names and ports printed by the script. Keep the script
running while the rebalancer runs; Ctrl+C stops its forwards. Without overrides,
the advertised shard endpoints must be directly reachable. The migration helper
still manages its own temporary root-shard port-forward.

Use `127.0.0.1`, not `localhost`, for these IPv4-only forwards: `localhost` may
resolve to `::1` and reach the kind/Podman front proxy on port 8443 instead.

Fresh deployments enable `LogicalClusterMigration=true` on the root, embedded
cache, and initial shards. For an existing cluster, run `make enable-migrations`
to enable it on the root and every shard and bind the migration API in `root`.
Run this before applying migrations.

### Configuration

All scripts resolve paths from the project directory, even when called from
another working directory. Credentials are generated with restrictive file
permissions and are ignored by Git.

| Environment variable | Default | Purpose |
| --- | --- | --- |
| `CLUSTER_NAME` | `kcp` | kind cluster name |
| `KIND_EXPERIMENTAL_PROVIDER` | `podman` | kind container provider; `docker` is also supported |
| `KCP_NAMESPACE` | `kcp` | KCP resources and etcd-druid namespace |
| `OPERATOR_NAMESPACE` | `kcp-operator` | KCP operator Helm namespace |
| `KIND_CONFIG` | `kind.kubeconfig` | Hosting kubeconfig filename under `.kube`, or an absolute path |
| `ADMIN_KUBECONFIG` | `admin.kubeconfig` | KCP admin kubeconfig filename under `.kube`, or an absolute path |
| `KCP_KUBECONFIG` | `kcp-wildcard.kubeconfig` | Rebalancer credential filename under `.kube`, or an absolute path |
| `DEPLOYMENT_TIMEOUT` | `600s` | Deployment and teardown wait timeout |
| `CERT_MANAGER_VERSION` | `v1.21.2` | cert-manager release |
| `KCP_VERSION` | `v0.33.1` | KCP image version |
| `ETCD_DRUID_VERSION` | `v0.39.0` | etcd-druid OCI Helm chart release |

`ETCD_IMAGE` and `ETCD_BACKUP_IMAGE` can override the images used by etcd.
The etcd-druid chart is installed from Gardener's OCI Helm registry, without
cloning its source repository. Operators are installed or updated with Helm.
cert-manager is applied from the versioned upstream release manifest; no
duplicated vendored manifest is needed in this project.

For example:

```sh
KIND_EXPERIMENTAL_PROVIDER=docker make deploy-infra
KCP_NAMESPACE=custom make deploy-kcp
KCP_NAMESPACE=custom make add-shard ARGS=shard-03
KUBECONFIG=.kube/kind.kubeconfig hack/generate-kcp-admin-kubeconfig.sh -n custom -p
```

Environment variables configure scripts; use `--kcp-kubeconfig` and
`--shard-server` to configure the Go program's credentials and endpoints.

### Teardown and existing installations

```sh
make teardown-kcp          # Delete KCP resources and uninstall both operators
make down                  # Delete the entire kind cluster and generated kubeconfigs
```

`make teardown-kcp` treats `KCP_NAMESPACE` as a dedicated environment: it deletes
all KCP operator resources, cert-manager Certificates/Issuers, and etcd resources
there, including added shards. It retains namespaces and storage volumes.
Do not point it at a namespace shared with unrelated installations.
Infrastructure teardown removes only the three configured kubeconfig files,
not unrelated files under `.kube`.

These scripts do not migrate an existing installation from `default` to `kcp`,
or uninstall an existing Prometheus release. The original project is untouched.
For a separate environment, use a different `CLUSTER_NAME`; concurrent local
clusters also need distinct host-port mappings (the provided kind setup uses
port 8443). For a clean replacement, explicitly tear down the old environment
first; that deletes its data. No cluster changes occur until you run a
deployment or teardown target.

## Build and test

```sh
make build
./bin/rebalance --help
make test
make vet
```

## Safety and behavior

- `root` and `system:*` logical clusters are excluded.
- Every discovered shard, including the root shard, can receive ordinary
  workspaces. Empty shards remain available as destinations.
- Nested workspaces count individually and migrate individually. Counts
  describe backing logical clusters, not Workspace objects in their parents.
- With the `simple` policy, target counts differ by at most one.
- Planned moves are rejected before printing if a workspace is not on the stated
  source shard, the destination is unknown or equal to the source, or a
  workspace is moved more than once.
- Non-Ready clusters, incomplete inventory entries, and duplicate identities
  stop the run.
- Apply mode checks for active migrations in root and verifies placement before
  each move. Migrations execute sequentially. Finally, the placement must match
  the plan and satisfy the selected policy.
- The default per-migration timeout is 10 minutes; override it with
  `--migration-timeout`.

Migration temporarily makes a workspace unavailable. Run only one rebalancer
and stop other placement-changing automation and workspace creation/deletion
during execution. There is no distributed lock or atomic cross-shard snapshot;
the checks cannot eliminate concurrent-change races.

On failure or timeout the CLI stops. Completed migrations are not rolled back,
and a submitted migration can continue after the CLI exits. Inspect it before
retrying. Completed migration objects are retained for audit.

## Kubernetes deployment status

This extraction preserves the local CLI's connection behavior. It does **not**
yet add in-cluster authentication, direct Service connections, a container image,
or deployment manifests.

The next step is to add hosting-cluster ServiceAccount authentication and direct
shard connectivity while retaining separate KCP credentials and TLS verification.

## Future features

Ideas to implement later, in no particular order.

- [ ] Protected shards and workspaces