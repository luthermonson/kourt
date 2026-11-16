package gate

import (
	"strings"
	"testing"

	"github.com/luthermonson/litecone/internal/plan"
	"github.com/luthermonson/litecone/internal/timeline"
)

func ev(id string, rev int64, op timeline.Op, key string) timeline.Event {
	return timeline.Event{ID: id, Rev: rev, Op: op, Key: key, BeadID: "b-" + id}
}

// A podinfo scale-up: Deployment -> ReplicaSet -> two new Pods, plus a
// stray configmap write elsewhere.
func scaleRun() *timeline.Run {
	const (
		dep = "/registry/deployments/default/podinfo"
		rs  = "/registry/replicasets/default/podinfo-c649"
		p1  = "/registry/pods/default/podinfo-c649-aaaa"
		p2  = "/registry/pods/default/podinfo-c649-bbbb"
		cm  = "/registry/configmaps/kube-system/stray"
	)
	return &timeline.Run{
		Source: "journal",
		Range:  timeline.Range{From: 100, To: 110},
		Events: []timeline.Event{
			ev("seq:1", 101, timeline.OpUpdate, dep),
			ev("seq:2", 102, timeline.OpUpdate, rs),
			ev("seq:3", 103, timeline.OpCreate, p1),
			ev("seq:4", 104, timeline.OpCreate, p2),
			ev("seq:5", 105, timeline.OpUpdate, p1),
			ev("seq:6", 106, timeline.OpUpdate, cm),
			ev("seq:7", 107, timeline.OpUpdate, dep),
		},
		Owners: map[string][]string{
			dep: {},
			rs:  {dep},
			p1:  {rs, dep},
			p2:  {rs, dep},
			cm:  {},
		},
	}
}

func parse(t *testing.T, src string) *plan.Plan {
	t.Helper()
	p, err := plan.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func find(v *Verdict, text string) *Check {
	for i := range v.Checks {
		if strings.Contains(v.Checks[i].Text, text) {
			return &v.Checks[i]
		}
	}
	return nil
}

func TestExpectOrderingPasses(t *testing.T) {
	p := parse(t, `# PLAN scale
## EXPECT
1. update deployments/default/podinfo
2. update replicasets/default/podinfo-*
3. create pods/default/podinfo-* x2
`)
	v := Judge(p, scaleRun())
	if !v.Pass {
		t.Fatalf("expected pass: %+v", v.Checks)
	}
	c := find(v, "create pods")
	if len(c.Evidence) != 2 || c.Evidence[0].EventID != "seq:3" || c.Evidence[1].EventID != "seq:4" {
		t.Fatalf("evidence = %+v", c.Evidence)
	}
}

func TestExpectOrderingViolation(t *testing.T) {
	// Pods before the ReplicaSet update: the ReplicaSet step can still be
	// satisfied, but the pod step must then find its creates AFTER it.
	p := parse(t, `# PLAN scale
## EXPECT
1. create pods/default/podinfo-* x2
2. update replicasets/default/podinfo-*
`)
	v := Judge(p, scaleRun())
	if v.Pass {
		t.Fatal("expected ordering failure")
	}
	c := find(v, "update replicasets")
	if c.Status != "fail" || !strings.Contains(c.Reason, "ordering violation") {
		t.Fatalf("check = %+v", c)
	}
}

func TestExpectCountShortfall(t *testing.T) {
	p := parse(t, "# PLAN x\n## EXPECT\n1. create pods/default/podinfo-* x3\n")
	v := Judge(p, scaleRun())
	if v.Pass || !strings.Contains(v.Checks[0].Reason, "found 2") {
		t.Fatalf("verdict = %+v", v.Checks[0])
	}
}

func TestThroughoutCounts(t *testing.T) {
	p := parse(t, `# PLAN x
## THROUGHOUT
- count create pods/default/podinfo-* == 2
- count update deployments/default/podinfo <= 1
- unchanged deployments/kube-system/coredns
- count any **/default/podinfo* >= 5
`)
	v := Judge(p, scaleRun())
	if find(v, "== 2").Status != "pass" {
		t.Fatal("create count should pass")
	}
	if c := find(v, "<= 1"); c.Status != "fail" || len(c.Evidence) != 2 {
		t.Fatalf("deployment updated twice should fail: %+v", c)
	}
	if find(v, "unchanged").Status != "pass" {
		t.Fatal("unchanged should pass")
	}
	if c := find(v, ">= 5"); c.Status != "pass" {
		t.Fatalf("** glob should match %+v", c)
	}
}

func TestNeverOutsideSubtree(t *testing.T) {
	p := parse(t, `# PLAN x
## NEVER
- outside deployments/default/podinfo
`)
	v := Judge(p, scaleRun())
	c := v.Checks[0]
	if c.Status != "fail" || len(c.Evidence) != 1 || c.Evidence[0].Key != "/registry/configmaps/kube-system/stray" {
		t.Fatalf("stray configmap should be the only offender: %+v", c)
	}
	// Excepting it makes ELSEWHERE quiet.
	p = parse(t, "# PLAN x\n## NEVER\n- outside deployments/default/podinfo except configmaps/kube-system/*\n")
	v = Judge(p, scaleRun())
	if !v.Pass {
		t.Fatalf("expected pass with exception: %+v", v.Checks[0])
	}
}

func TestNeverOp(t *testing.T) {
	p := parse(t, "# PLAN x\n## NEVER\n- delete pods/default/podinfo-*\n- update configmaps/**\n")
	v := Judge(p, scaleRun())
	if v.Checks[0].Status != "pass" {
		t.Fatal("no deletes happened")
	}
	if v.Checks[1].Status != "fail" || v.Checks[1].Evidence[0].EventID != "seq:6" {
		t.Fatalf("configmap update should be cited: %+v", v.Checks[1])
	}
}

func TestProseIsSkippedAndLossyNoted(t *testing.T) {
	p := parse(t, "# PLAN x\n## EXPECT\n1. the pods become Ready\n2. create pods/default/podinfo-* x2\n")
	r := scaleRun()
	r.Lossy = true
	v := Judge(p, r)
	if !v.Pass || v.Skipped != 1 || v.Checks[0].Status != "skip" {
		t.Fatalf("prose should skip, not fail: %+v", v)
	}
	if len(v.Notes) == 0 || !strings.Contains(v.Notes[0], "lossy") {
		t.Fatal("lossy run must be noted in the verdict")
	}
}

func TestParseErrorFailsTheGate(t *testing.T) {
	p := parse(t, "# PLAN x\n## EXPECT\n1. create pods/a/b x0\n")
	v := Judge(p, scaleRun())
	if v.Pass || !strings.Contains(v.Checks[0].Reason, "does not parse") {
		t.Fatalf("unparsable directive must fail: %+v", v.Checks)
	}
}

func TestMatch(t *testing.T) {
	cases := []struct {
		glob, key string
		want      bool
	}{
		{"/registry/pods/default/podinfo-*", "/registry/pods/default/podinfo-abc", true},
		{"/registry/pods/default/podinfo-*", "/registry/pods/other/podinfo-abc", false},
		{"/registry/pods/*/podinfo-*", "/registry/pods/other/podinfo-abc", true},
		{"/registry/pods/**", "/registry/pods/a/b", true},
		{"/registry/pods/**", "/registry/pods", true},
		{"/registry/**/podinfo", "/registry/deployments/default/podinfo", true},
		{"/registry/pods/default/x", "/registry/pods/default/x/y", false},
		{"/registry/services/specs/default/*", "/registry/services/specs/default/podinfo", true},
	}
	for _, c := range cases {
		if got := Match(c.glob, c.key); got != c.want {
			t.Errorf("Match(%q,%q) = %v", c.glob, c.key, got)
		}
	}
}

func TestInSubtree(t *testing.T) {
	root := "/registry/deployments/default/podinfo"
	if !InSubtree(root, root, nil) {
		t.Fatal("root is in its own subtree")
	}
	if !InSubtree(root, "/registry/pods/default/p", []string{"/registry/replicasets/default/r", root}) {
		t.Fatal("grandchild via owner chain")
	}
	if InSubtree(root, "/registry/pods/default/p", []string{"/registry/replicasets/default/other"}) {
		t.Fatal("unrelated owner chain")
	}
}
