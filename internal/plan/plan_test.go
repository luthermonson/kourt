package plan

import (
	"strings"
	"testing"
	"time"
)

const sample = `# PLAN capi-autoscale-up

Preamble prose, ignored.

## INTENT
Four unschedulable pods make the autoscaler scale demo-md.

## SUBTREE
- machinedeployments/demo-md

## GIVEN
(a provisioned cluster)
- wait 5s

## WHEN
- apply manifests/pending-pods.yaml   # the stimulus

## EXPECT
[pending]
- pods/scale-test-* ×4 created
- pods/scale-test-* condition PodScheduled=False

[scale] then
- machinedeployments/demo-md .spec.replicas 1→3
- machinedeployments/demo-md updated
- machines/demo-md-* x2 created-by machinesets/demo-md-*
- miragemachines/demo-md-* ×2+ created

[nodes] after scale
- nodes/demo-md-* ×2 created

- machines/demo-md-* phase Pending->Running after nodes
- finally clusters/demo condition Ready=True
- the pods eventually bind (prose)

## THROUGHOUT
- machines/demo-md-* ×0 deleted
- no machine flaps (prose)

## NEVER
- elsewhere changed except pods/scale-test-*, nodes/*, leases/*
- machinedeployments/demo-md deleted
- the ONLY actor writing machines is mirage
- machines/demo-cp-* phase Running→Failed

## SETTLE
- quiescence 20s on the subtree
- timeout 10m

## EVIDENCE
Cite revisions.
`

func TestParse(t *testing.T) {
	p, err := Parse(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "capi-autoscale-up" {
		t.Fatalf("name = %q", p.Name)
	}
	if roots := p.Roots(); len(roots) != 1 || roots[0].Kind != "machinedeployments" || roots[0].Name != "demo-md" {
		t.Fatalf("roots = %+v", roots)
	}
	if a := p.Actions(SecGiven); len(a) != 1 || a[0].Verb != "wait" || a[0].Wait != 5*time.Second {
		t.Fatalf("GIVEN actions = %+v", a)
	}
	if a := p.Actions(SecWhen); len(a) != 1 || a[0].Verb != "apply" || a[0].Path != "manifests/pending-pods.yaml" {
		t.Fatalf("WHEN actions = %+v (comment not stripped?)", a)
	}

	ex := p.Section(SecExpect)
	// [pending]
	if ex[0].Group == nil || ex[0].Group.Label != "pending" || len(ex[0].Group.Orders) != 0 {
		t.Fatalf("group header 1 = %+v", ex[0])
	}
	if s := ex[1].Step; s == nil || s.Group != "pending" || s.Count != 4 || s.AtLeast || s.What != Created || s.Ref.Kind != "pods" || s.Ref.Name != "scale-test-*" {
		t.Fatalf("step 1 = %+v", ex[1])
	}
	if s := ex[2].Step; s == nil || s.What != Condition || !ex[2].Skipped {
		t.Fatalf("condition must parse as SKIPPED: %+v", ex[2])
	}
	// [scale] then
	if g := ex[3].Group; g == nil || g.Label != "scale" || len(g.Orders) != 1 || g.Orders[0].Kind != "then" {
		t.Fatalf("group header 2 = %+v", ex[3])
	}
	if s := ex[4].Step; s == nil || s.What != Field || !ex[4].Skipped {
		t.Fatalf("field transition must be SKIPPED: %+v", ex[4])
	}
	if s := ex[5].Step; s == nil || s.What != Updated || s.Group != "scale" {
		t.Fatalf("updated = %+v", ex[5])
	}
	if s := ex[6].Step; s == nil || s.What != CreatedBy || s.Count != 2 || s.Owner == nil || s.Owner.Kind != "machinesets" || s.Owner.Name != "demo-md-*" {
		t.Fatalf("created-by = %+v", ex[6])
	}
	if s := ex[7].Step; s == nil || s.Count != 2 || !s.AtLeast {
		t.Fatalf("×2+ = %+v", ex[7])
	}
	// [nodes] after scale
	if g := ex[8].Group; g == nil || g.Orders[0].Kind != "after" || g.Orders[0].Target != "scale" {
		t.Fatalf("group header 3 = %+v", ex[8])
	}
	// trailing `after nodes` on a skipped line
	if s := ex[10].Step; s == nil || s.What != Phase || len(s.Orders) != 1 || s.Orders[0].Kind != "after" || s.Orders[0].Target != "nodes" {
		t.Fatalf("trailing after = %+v", ex[10])
	}
	if s := ex[11].Step; s == nil || len(s.Orders) != 1 || s.Orders[0].Kind != "finally" {
		t.Fatalf("leading finally = %+v", ex[11])
	}
	if !ex[12].Prose {
		t.Fatalf("prose line = %+v", ex[12])
	}

	thr := p.Section(SecThroughout)
	if thr[0].Inv == nil || thr[0].Inv.Count != 0 || thr[0].Inv.What != Deleted {
		t.Fatalf("invariant = %+v", thr[0])
	}
	if !thr[1].Prose {
		t.Fatal("prose invariant")
	}

	nv := p.Section(SecNever)
	if nv[0].Never == nil || nv[0].Never.Kind != "elsewhere" || len(nv[0].Never.Excepts) != 3 || nv[0].Never.Excepts[2].Kind != "leases" {
		t.Fatalf("elsewhere = %+v", nv[0].Never)
	}
	if nv[1].Never == nil || nv[1].Never.Kind != "matcher" || nv[1].Never.Matcher.What != Deleted {
		t.Fatalf("never deleted = %+v", nv[1].Never)
	}
	if !nv[2].Skipped || nv[2].Never.Matcher.What != Provenance {
		t.Fatalf("provenance NEVER must be SKIPPED: %+v", nv[2])
	}
	if !nv[3].Skipped || nv[3].Never.Matcher.What != Phase {
		t.Fatalf("field NEVER must be SKIPPED: %+v", nv[3])
	}

	q, to := p.Quiescence()
	if q != 20*time.Second || to != 10*time.Minute {
		t.Fatalf("settle = %v %v", q, to)
	}
	machine, skipped, prose, errs := p.Counts()
	if errs != 0 || machine != 8 || skipped != 6 || prose < 3 {
		t.Fatalf("counts machine=%d skipped=%d prose=%d errs=%d", machine, skipped, prose, errs)
	}
}

func TestParseErrors(t *testing.T) {
	src := `# PLAN x
## SUBTREE
- not-a-ref
## EXPECT
- machines/x ×2 exploded
- [bad label] after
- machines/x ×2
## SETTLE
- quiescence soon
## NEVER
- elsewhere changed except nonsense
`
	p, err := Parse(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, errs := p.Counts()
	if errs != 5 {
		for _, l := range p.Lines {
			t.Logf("L%d %q err=%q prose=%v skip=%v", l.LineNo, l.Text, l.Err, l.Prose, l.Skipped)
		}
		t.Fatalf("errors = %d, want 5", errs)
	}
}

func TestDefaults(t *testing.T) {
	p, _ := Parse(strings.NewReader("## WHEN\n- apply a.yaml\n"))
	if p.Name != "unnamed" {
		t.Fatal("default name")
	}
	q, to := p.Quiescence()
	if q != 10*time.Second || to != 5*time.Minute {
		t.Fatal("default settle")
	}
}
