package gate

import (
	"strings"
	"testing"
	"time"
)

func TestSuiteExitCodes(t *testing.T) {
	empty := &Suite{}
	if empty.ExitCode() != ExitError {
		t.Fatal("an empty suite must not pass: nothing was tested")
	}
	s := &Suite{}
	s.Add(&Verdict{Pass: true})
	if s.ExitCode() != ExitPass {
		t.Fatal("one passing plan -> 0")
	}
	s.Add(&Verdict{Pass: false})
	if s.ExitCode() != ExitFail {
		t.Fatal("any failing plan -> 1")
	}
	s.AddError("x.plan.md", errString("boom"))
	if s.ExitCode() != ExitError {
		t.Fatal("any plan that could not run -> 2, even with failures present")
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestFailSettleIsAGateFailure(t *testing.T) {
	v := &Verdict{Pass: true}
	FailSettle(v, 10*time.Second, time.Minute, 4242)
	if v.Pass || v.Failed != 1 || v.Checks[0].Section != "SETTLE" || !strings.Contains(v.Checks[0].Reason, "4242") {
		t.Fatalf("settle timeout must fail the verdict: %+v", v)
	}
}

func TestFailClosedOnEmptyPlan(t *testing.T) {
	p := parse(t, "# PLAN empty\n## INTENT\nnothing checkable\n")
	v := Judge(p, scaleRun())
	if v.Pass || !strings.Contains(v.Checks[len(v.Checks)-1].Reason, "no machine-checkable") {
		t.Fatalf("empty plan must fail closed: %+v", v.Checks)
	}
}

func TestExplicitNothingHappenedPasses(t *testing.T) {
	// The only way to assert "nothing happened" is to say so.
	p := parse(t, "# PLAN quiet\n## THROUGHOUT\n- count any ** == 0\n")
	r := scaleRun()
	r.Events = nil
	if v := Judge(p, r); !v.Pass {
		t.Fatalf("explicit zero-count on an empty window must pass: %+v", v.Checks)
	}
	// ...and it fails when something did happen.
	if v := Judge(p, scaleRun()); v.Pass {
		t.Fatal("explicit zero-count must fail when events exist")
	}
}

func TestFailureScenarioPlan(t *testing.T) {
	// "apply an invalid CR -> the controller creates NO children": the gate
	// must FAIL if children appear.
	p := parse(t, "# PLAN invalid-cr\n## EXPECT\n1. update deployments/default/podinfo\n## NEVER\n- create pods/default/podinfo-*\n")
	if v := Judge(p, scaleRun()); v.Pass {
		t.Fatal("children appeared; the NEVER must trip")
	}
	r := scaleRun()
	r.Events = r.Events[:1] // only the deployment update
	if v := Judge(p, r); !v.Pass {
		t.Fatalf("no children -> pass: %+v", v.Checks)
	}
}
