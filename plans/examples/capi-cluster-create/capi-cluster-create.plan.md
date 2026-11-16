# PLAN capi-cluster-create

## INTENT
A Cluster API cluster backed by the mirage fake provider goes from an
applied template to Ready through the real controller chain — Cluster →
MirageCluster infra, KubeadmControlPlane → control-plane Machine →
MirageMachine, MachineDeployment → MachineSet → worker Machines → Nodes —
with every child created inside the Cluster's ownership subtree, no Machine
ever deleted, and nothing outside the cluster's light cone touched. The
field-level transitions (infra Ready, phase trail, control-plane endpoint)
are stated for the narrative judge; they are reported SKIPPED by the gate
until value decoding lands.

## SUBTREE
- clusters/demo

## GIVEN
(the mirage provider, the Kubeadm control-plane / bootstrap providers and CAPI core are installed and quiet)

## WHEN
- apply manifests/demo-cluster.yaml

## EXPECT
[apply]
- clusters/demo created
- mirageclusters/demo created
- kubeadmcontrolplanes/demo-cp created
- machinedeployments/demo-md created

[infra] then
- mirageclusters/demo condition Ready=True
- clusters/demo .status.controlPlaneEndpoint set
- clusters/demo condition InfrastructureReady=True

[controlplane] after apply
- machines/demo-cp-* ×1 created-by kubeadmcontrolplanes/demo-cp
- miragemachines/demo-cp-* ×1 created
- machines/demo-cp-* phase Pending→Provisioning→Provisioned→Running
- kubeadmcontrolplanes/demo-cp condition Available=True

[workers] concurrent with controlplane
- machinesets/demo-md-* ×1 created-by machinedeployments/demo-md
- machines/demo-md-* ×2 created-by machinesets/demo-md-*
- miragemachines/demo-md-* ×2 created

[nodes] after controlplane
- nodes/demo-cp-* ×1 created
- nodes/demo-md-* ×2 created

[ready] finally
- clusters/demo phase Provisioning→Provisioned
- clusters/demo condition Ready=True
- machinedeployments/demo-md .status.readyReplicas set

## THROUGHOUT
- machines/demo-* ×3 created
- machines/demo-* ×0 deleted
- machinesets/demo-md-* ×1 created
- mirageclusters/demo ×1 created
- kubeadmcontrolplanes/demo-cp condition Available never flips back to False

## NEVER
- elsewhere changed except nodes/*, leases/*, events/*, endpointslices/*, masterleases/*, peerserverleases/*
- machines/demo-cp-* deleted
- clusters/demo deleted
- the ONLY actor writing machines/* status is the mirage controller

## SETTLE
- quiescence 15s on the subtree
- timeout 5m

## EVIDENCE
- Each `created` line: the create event id and revision.
- Each ordering edge: the two revisions compared (latest of the earlier group, earliest of the later).
- NEVER elsewhere: zero offenders, or every offender with its key and revision.
- The judge cites phase / condition claims from decoded values when decoding exists, and marks them "cannot judge" until then.
