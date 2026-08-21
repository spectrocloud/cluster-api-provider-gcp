# CAPI v1.12.0 Subnet/Dual-Stack Parity — Drift-Diff (spectro fork → upstream native)

**Summary: 0 GAPs, 1 BEHAVIORAL-DIFF.** Dropping the spectro custom subnet commits in favor of
upstream v1.12.0's native `cloud/services/compute/subnets/` package is a **near-total functional
upgrade** — every capability the spectro `SubnetworkSpec()` path provided is COVERED (and exceeded)
by upstream `SubnetSpecs()` + the `subnets` reconciler. The only behavioral difference is the
**subnet-creation gating condition** (spectro gated on `!network.AutoCreateSubnetworks`; upstream
always reconciles subnets). No functionality is lost; review the one BEHAVIORAL-DIFF.

## Key structural finding

The spectro commits added a SECOND, parallel subnet path inside the `networks` package
(`ClusterScope.SubnetworkSpec()` / `ManagedClusterScope.SubnetworkSpec()` +
`networks.createOrGetSubNetwork`). Meanwhile the fork ALSO already carries upstream's richer
`SubnetSpecs()` method and the full upstream `SubnetSpec` API type (commits 615e368e and 2e9689fc
actually modify the *upstream-style* `SubnetSpecs()` / `SubnetSpec`, not the spectro custom path).

- Spectro custom path: `origin/spectro-release-4.9:cloud/services/compute/networks/reconcile.go:48-55, 184-208` and `cloud/scope/cluster.go:231`, `cloud/scope/managedcluster.go:215`.
- Upstream native path (v1.12.0): `cloud/services/compute/subnets/{reconcile,service}.go`, `cloud/scope/cluster.go:266 SubnetSpecs()`, `cloud/scope/managedcluster.go SubnetSpecs()`.
- The fork's `api/v1beta1/types.go SubnetSpec` is **already byte-identical** to `v1.12.0:api/v1beta1/types.go SubnetSpec` (Description, SecondaryCidrBlocks, PrivateGoogleAccess, EnableFlowLogs, Purpose, StackType all present). So the API surface is unchanged by the upgrade.

Spectro's `SubnetworkSpec()` only ever populated **5 fields** (Name, Region, IpCidrRange, Network,
EnableFlowLogs). Upstream's `SubnetSpecs()` populates **11 fields** (adds PrivateIpGoogleAccess,
SecondaryIpRanges, Description, Purpose, Role, StackType). Upstream is a strict superset.

## Capability matrix

| Capability | Spectro (commit) | Upstream v1.12.0 | Verdict | Notes/risk |
|---|---|---|---|---|
| Custom subnet creation, self-managed | `SubnetworkSpec()` + `createOrGetSubNetwork` (01e06d72) wired in `networks` pkg | `subnets.New(clusterScope)` reconciler, wired in `controllers/gcpcluster_controller.go:205` | COVERED | Upstream uses a dedicated reconciler; create-or-get semantics equivalent. |
| Custom subnet creation, managed (GKE) | `ManagedClusterScope.SubnetworkSpec()` (d8e22c72) | `subnets.New(clusterScope)` wired in `exp/controllers/gcpmanagedcluster_controller.go:200` | COVERED | Same `subnets` pkg reused for managed; reconcile + delete both wired (200/252). |
| Subnet delete | spectro `networks.Delete` loop (01e06d72) | `subnets.Service.Delete` (`subnets/reconcile.go:43`), wired at gcpcluster_controller.go:236 / gcpmanagedcluster_controller.go:252 | COVERED | Upstream is SAFER: skips delete if subnet not CAPG-created (description check) and skips on Shared VPC. Spectro deleted unconditionally. |
| Idempotent create-or-get | `createOrGetSubNetwork` (01e06d72) | `createOrGetSubnets` (`subnets/reconcile.go:81`) | COVERED | Identical Get→Insert→Get pattern. Neither updates/patches an existing subnet (no StackType/flowlogs reconcile-on-change in either). |
| `EnableFlowLogs` field | set from `*subnet.EnableFlowLogs` (01e06d72) | `EnableFlowLogs: ptr.Deref(subnetwork.EnableFlowLogs, false)` (cluster.go:266 / managedcluster.go) | COVERED | Same field, same effective default (false when nil). |
| `EnableFlowLogs` nil-check (panic guard) | `if subnet.EnableFlowLogs != nil` (8a9fbde4) | `ptr.Deref(..., false)` | COVERED | Upstream's `ptr.Deref` is the safer equivalent of the spectro nil-guard; no panic risk. |
| `StackType` / dual-stack on subnet | `StackType: subnetwork.StackType` in `SubnetSpecs()` (615e368e) | `StackType: subnetwork.StackType` in `SubnetSpecs()` (cluster.go:266 / managedcluster.go) | COVERED | Identical line; this commit already targeted the upstream-style method, so it merges cleanly / is already present upstream. |
| `StackType` API field + dual-stack enum/default | `SubnetSpec.StackType` enum IPV4_ONLY/IPV4_IPV6/IPV6_ONLY, default IPV4_ONLY (2e9689fc) | identical in `v1.12.0:api/v1beta1/types.go` | COVERED | API type byte-identical to fork; CRDs already carry the enum. No change. |
| `k8scloud.Option` variadic on subnet iface | added options to Get/Insert/Delete (5dff97d6) | `subnets/service.go subnetsInterface` already has `options ...k8scloud.Option` | COVERED | Upstream interface already matches; spectro fix becomes obsolete (was a compile fix against the spectro custom iface, which is deleted). |
| Secondary IP ranges | **NOT present** in spectro `SubnetworkSpec()` | `SecondaryIpRanges` built from `SecondaryCidrBlocks` (cluster.go:266) | COVERED (upstream ADDS) | Net gain, not a loss. |
| Purpose / PrivateGoogleAccess / Description / Role | **NOT present** in spectro `SubnetworkSpec()` | all set in upstream `SubnetSpecs()` | COVERED (upstream ADDS) | Net gain, not a loss. |
| Shared-VPC subnet handling | none in spectro path | `IsSharedVpc()` branches in `subnets/service.go New` + reconcile/delete | COVERED (upstream ADDS) | Net gain. |
| Subnet creation gating | spectro only creates subnets when `!network.AutoCreateSubnetworks` (custom-mode VPC) — `networks/reconcile.go:48` | upstream `subnets.Reconcile` runs unconditionally for every entry in `Spec.Network.Subnets` | **BEHAVIORAL-DIFF** | See below. Low risk in practice but the only true behavior change. |

## ⚠️ Functionality at risk (review before release)

### BEHAVIORAL-DIFF 1 — Subnet-creation gating condition (the only behavior change)

- **Spectro** (`origin/spectro-release-4.9:cloud/services/compute/networks/reconcile.go:48-55`):
  subnets are created **only if** `!network.AutoCreateSubnetworks` (i.e. only when the VPC is in
  custom subnet mode). In auto-subnet-mode VPCs, `Spec.Network.Subnets` entries are silently ignored.
- **Upstream v1.12.0** (`cloud/services/compute/subnets/reconcile.go:81 createOrGetSubnets`):
  the `subnets` reconciler iterates **every** entry in `Spec.Network.Subnets` and create-or-gets it,
  with **no `AutoCreateSubnetworks` guard**.
- **Effect of adopting upstream:** if a user defined `Spec.Network.Subnets` on an
  auto-mode VPC, upstream will now attempt to create those subnets where spectro skipped them.
  This is almost certainly the *correct* behavior (and matches CAPI intent), and create-or-get is
  idempotent against an existing auto-created subnet, but it is a genuine behavioral change worth a
  human nod — especially for any existing clusters that relied on the spectro skip.
- **Risk: LOW.** No data loss; worst case is an extra subnet create attempt on auto-mode VPCs.

### Non-issues confirmed (not gaps)

- **EnableFlowLogs nil panic (8a9fbde4):** upstream uses `ptr.Deref(subnetwork.EnableFlowLogs, false)`
  — no panic risk; the spectro nil-guard is redundant under upstream.
- **k8scloud.Option fix (5dff97d6):** was a compile fix for the spectro custom `subnetworksInterface`
  inside the `networks` pkg, which upstream deletes entirely (`networks` pkg no longer touches
  subnets in v1.12.0). The upstream `subnets.subnetsInterface` already declares the variadic options.
- **No idempotent UPDATE in either implementation:** neither spectro nor upstream patches an existing
  subnet's StackType/EnableFlowLogs after creation. Changing those fields on an existing cluster is a
  no-op in BOTH — so adopting upstream introduces no regression here.

## Bottom line

Adopting upstream v1.12.0's native subnet/dual-stack implementation **loses no functionality** and
gains several capabilities (secondary ranges, Purpose, PrivateGoogleAccess, Description, Role,
Shared-VPC awareness, safer delete). The single thing to flag to a human is the dropped
`!network.AutoCreateSubnetworks` gate (BEHAVIORAL-DIFF 1).
