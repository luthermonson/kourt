package plan

import (
	"strings"
	"testing"
	"time"
)

const sample = `# PLAN podinfo-scale

Some preamble prose that is ignored.

## INTENT
Scaling podinfo to 3 replicas creates exactly two new pods under its ReplicaSet and touches nothing else.

## GIVEN
- apply manifests/podinfo.yaml

## WHEN
- apply manifests/podinfo-3.yaml   # scale up

## EXPECT (timeline -- order matters)
1. update deployments/default/podinfo
2. update replicasets/default/podinfo-*
3. create pods/default/podinfo-* x2  -- the two new replicas
4. the pods become Ready (narrative only)

## THROUGHOUT
- count create pods/default/podinfo-* == 2
- unchanged deployments/kube-system/coredns
- no pod flaps (prose)

## NEVER
- outside deployments/default/podinfo except events/default/*, leases/**
- delete pods/default/podinfo-*

## SETTLE
- quiet 15s
- timeout 3m

## EVIDENCE
Cite the create events for line 3 with revisions.
`

func TestParse(t *testing.T) {
	p, err := Parse(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "podinfo-scale" {
		t.Fatalf("name = %q", p.Name)
	}
	if got := p.Actions(SecGiven); len(got) != 1 || got[0].Verb != "apply" || got[0].Path != "manifests/podinfo.yaml" {
		t.Fatalf("GIVEN actions = %+v", got)
	}
	if got := p.Actions(SecWhen); len(got) != 1 || got[0].Path != "manifests/podinfo-3.yaml" {
		t.Fatalf("WHEN actions = %+v (comment not stripped?)", got)
	}
	steps := p.Steps()
	if len(steps) != 3 {
		t.Fatalf("steps = %d, want 3 (prose line must not be a step)", len(steps))
	}
	if steps[2].Step.Count != 2 || steps[2].Step.Ref != "/registry/pods/default/podinfo-*" || steps[2].Step.Op != OpCreate {
		t.Fatalf("step 3 = %+v", steps[2].Step)
	}
	expect := p.Section(SecExpect)
	if !expect[3].Prose {
		t.Fatal("line 4 of EXPECT should be prose")
	}
	thr := p.Section(SecThroughout)
	if thr[0].Invariant == nil || thr[0].Invariant.Kind != "count" || thr[0].Invariant.Cmp != "==" || thr[0].Invariant.N != 2 {
		t.Fatalf("invariant 1 = %+v", thr[0].Invariant)
	}
	if thr[1].Invariant == nil || thr[1].Invariant.Kind != "unchanged" || thr[1].Invariant.Ref != "/registry/deployments/kube-system/coredns" {
		t.Fatalf("invariant 2 = %+v", thr[1].Invariant)
	}
	if !thr[2].Prose {
		t.Fatal("invariant 3 should be prose")
	}
	never := p.Section(SecNever)
	if never[0].Never == nil || never[0].Never.Kind != "outside" || never[0].Never.Ref != "/registry/deployments/default/podinfo" {
		t.Fatalf("never 1 = %+v", never[0].Never)
	}
	if len(never[0].Never.Excepts) != 2 || never[0].Never.Excepts[0] != "/registry/events/default/*" || never[0].Never.Excepts[1] != "/registry/leases/**" {
		t.Fatalf("excepts = %v", never[0].Never.Excepts)
	}
	if never[1].Never == nil || never[1].Never.Kind != "op" || never[1].Never.Op != OpDelete {
		t.Fatalf("never 2 = %+v", never[1].Never)
	}
	q, to := p.Quiet()
	if q != 15*time.Second || to != 3*time.Minute {
		t.Fatalf("settle = %v %v", q, to)
	}
	if !strings.Contains(p.Prose(SecIntent), "exactly two new pods") {
		t.Fatal("intent prose lost")
	}
}

func TestParseErrors(t *testing.T) {
	p, err := Parse(strings.NewReader("# PLAN x\n## EXPECT\n- create pods/a/b x0\n- create pods/a/b twice\n## THROUGHOUT\n- count create pods/a/b ~ 2\n## SETTLE\n- quiet soon\n"))
	if err != nil {
		t.Fatal(err)
	}
	errs := 0
	for _, l := range p.Lines {
		if l.Err != "" {
			errs++
		}
	}
	if errs != 4 {
		for _, l := range p.Lines {
			t.Logf("%d %q err=%q prose=%v", l.LineNo, l.Text, l.Err, l.Prose)
		}
		t.Fatalf("errors = %d, want 4", errs)
	}
}

func TestDefaults(t *testing.T) {
	p, _ := Parse(strings.NewReader("## WHEN\n- apply a.yaml\n"))
	if p.Name != "unnamed" {
		t.Fatal("default name")
	}
	q, to := p.Quiet()
	if q != 10*time.Second || to != 5*time.Minute {
		t.Fatal("default settle")
	}
}
