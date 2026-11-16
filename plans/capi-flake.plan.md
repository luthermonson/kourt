# PLAN capi-no-orphan-nodes

## INTENT
A correct Cluster API provider registers exactly one fake Node per Machine and
never leaks an orphan. Applying the mirage flake cluster (3 control-plane + 3
worker Machines) must bring up exactly six Nodes for this cluster — one per
Machine — and no extras. An extra, unowned Node is leaked infrastructure: in
the real world that is a VM still running that nobody is tracking or paying
for. This plan asserts the one-Node-per-Machine invariant. A provider that
occasionally double-provisions a VM trips it — and because the double-provision
is random, it trips only on some runs. That is the signature of a flake, and
the committed timeline is what lets the judge tell a flake from a real bug.

## SUBTREE
- clusters/mirage-flake

## WHEN
- apply /Users/luthermonson/cluster-api-provider-mirage/examples/flake-cluster.yaml

## EXPECT
[apply]
- clusters/mirage-flake created
- miragecontrolplanes/mirage-flake-cp created
- machinedeployments/mirage-flake-* ×1+ created

[machines] after apply
- machines/mirage-flake-* ×6+ created
- miragemachines/mirage-flake-* ×6+ created

## THROUGHOUT
- machines/mirage-flake-* ×6+ created
- nodes/mirage-flake-* ×6 created
- nodes/mirage-flake-*-ghost ×0 created

## SETTLE
- quiescence 30s on the subtree
- timeout 5m

## EVIDENCE
- nodes created: the create event id and revision for each.
- any count above six, or any -ghost node: every offending node key with its revision.
