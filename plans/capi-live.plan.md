# PLAN capi-cluster-create-live

## INTENT
Applying the mirage quickstart cluster brings a full CAPI cluster to life through the
real controller chain — Cluster → MirageCluster infra, MirageControlPlane → 3 control-plane
Machines → MirageMachine, MachineDeployment → MachineSet → 3 worker Machines → fake Nodes —
every child created inside the Cluster's ownership subtree, no Machine ever deleted, and
nothing outside the cluster's light cone touched. Field-level transitions are stated for the
narrative judge and are reported SKIPPED by the gate until value decoding lands.

## SUBTREE
- clusters/mirage-quickstart

## GIVEN
(CAPI core and the mirage fake provider are installed and quiet)

## WHEN
- apply /Users/luthermonson/cluster-api-provider-mirage/examples/quickstart-cluster.yaml

## EXPECT
[apply]
- clusters/mirage-quickstart created
- mirageclusters/mirage-quickstart created
- miragecontrolplanes/mirage-quickstart-cp created
- machinedeployments/mirage-quickstart-* ×1+ created

[machines] after apply
- machinesets/mirage-quickstart-* ×1+ created
- machines/mirage-quickstart-* ×6+ created
- miragemachines/mirage-quickstart-* ×6+ created

## THROUGHOUT
- machines/mirage-quickstart-* ×6+ created
- machines/mirage-quickstart-* ×0 deleted
- machinesets/mirage-quickstart-* ×1 created
- mirageclusters/mirage-quickstart ×1 created

## NEVER
- elsewhere changed except nodes/*, leases/*, events/*, endpointslices/*, masterleases/*, peerserverleases/*, miragemachinetemplates/*, mirageconfigtemplates/*, miragecontrolplanetemplates/*, mirageconfigs/*, secrets/*, pods/*, services/*, daemonsets/*
- clusters/mirage-quickstart deleted

## SETTLE
- quiescence 30s on the subtree
- timeout 5m

## EVIDENCE
- Each created line: the create event id and revision.
- NEVER elsewhere: zero offenders, or every offender with key and revision.
