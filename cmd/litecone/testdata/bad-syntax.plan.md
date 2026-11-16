# PLAN fixture-bad-syntax

## EXPECT
1. create pods/default/podinfo-* x0
2. create pods/default/podinfo-* twice over

## THROUGHOUT
- count create pods/default/podinfo-* ~ 2
