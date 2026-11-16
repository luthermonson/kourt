package gate

import (
	"strings"
	"testing"
	"time"

	"github.com/luthermonson/litecone/internal/timeline"
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
	s.Add(&Verdict{Pass: true, Skipped: 3})
	if s.ExitCode() != ExitPass {
		t.Fatal("SKIPPED assertions do not fail CI")
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
	v := Judge(p, newTL().run(0, 1))
	if v.Pass || !strings.Contains(v.Checks[len(v.Checks)-1].Reason, "no structural") {
		t.Fatalf("empty plan must fail closed: %+v", v.Checks)
	}
}

func TestFailClosedWhenEverythingIsSkipped(t *testing.T) {
	// Only field-level assertions: nothing structural was evaluated, so the
	// plan cannot go green on SKIPPED alone.
	p := parse(t, "# PLAN fields\n## SUBTREE\n- clusters/demo\n## EXPECT\n- clusters/demo phase Pending→Provisioned\n- clusters/demo condition Ready=True\n")
	v := Judge(p, clusterCreate().run(1000, 1100))
	if v.Pass || v.Skipped != 2 {
		t.Fatalf("all-skipped plan must fail closed with the skips visible: pass=%v skipped=%d", v.Pass, v.Skipped)
	}
	if !strings.Contains(v.Checks[len(v.Checks)-1].Reason, "all 2 assertions are SKIPPED") {
		t.Fatalf("reason: %s", v.Checks[len(v.Checks)-1].Reason)
	}
}

func TestFailureScenarioPlan(t *testing.T) {
	// "apply an invalid CR -> the controller creates NO children": the gate
	// must FAIL if children appear.
	p := parse(t, `# PLAN invalid-cr
## SUBTREE
- clusters/demo
## EXPECT
- clusters/demo created
## THROUGHOUT
- machines/demo-* ×0 created
## NEVER
- machinesets/demo-* created
`)
	if v := Judge(p, clusterCreate().run(1000, 1100)); v.Pass {
		t.Fatal("children appeared; the NEVER and the ×0 must trip")
	}
	only := newTL()
	only.ev(1001, timeline.OpCreate, kCluster)
	if v := Judge(p, only.run(1000, 1100)); !v.Pass {
		dump(t, v)
		t.Fatal("no children -> pass")
	}
}
