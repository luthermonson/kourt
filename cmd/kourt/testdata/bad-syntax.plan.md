# PLAN fixture-bad-syntax

## SUBTREE
- deployments/podinfo

## EXPECT
- pods/podinfo-* ×2 exploded
- [group label] after
- pods/podinfo-* ×2

## THROUGHOUT
- pods/podinfo-* ×2 created
