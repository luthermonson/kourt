# PLAN smoke-configmap

## INTENT
Applying a plain ConfigMap writes exactly one new object and causes no
controller to do anything. This is the "lights on" plan for a fresh
k3s-on-beads substrate: it proves the apply reaches kine, the bead appears
with a revision, quiescence on the subtree is reached, and the capture/gate
loop works end to end. Node leases, apiserver peer leases, EndpointSlices
and Events keep churning in the background; they are excepted explicitly
rather than ignored silently.

## SUBTREE
- configmaps/litecone-smoke

## GIVEN
(nothing — the cluster as it stands)

## WHEN
- apply manifests/configmap.yaml

## EXPECT
- configmaps/litecone-smoke created

## THROUGHOUT
- configmaps/litecone-smoke ×1 changed
- configmaps/litecone-smoke ×0 deleted

## NEVER
- elsewhere changed except nodes/*, leases/*, events/*, endpointslices/*, services/*, masterleases/*, peerserverleases/*
- configmaps/* deleted

## SETTLE
- quiescence 5s on the subtree
- timeout 1m

## CLEANUP
- delete manifests/configmap.yaml

## EVIDENCE
The create event for configmaps/litecone-smoke, with its revision; zero offenders for `elsewhere`.
