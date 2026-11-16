package gate

import (
	"fmt"
	"time"

	"github.com/luthermonson/litecone/internal/plan"
)

// Exit codes: the binary's CI contract.
const (
	ExitPass  = 0 // every plan passed
	ExitFail  = 1 // at least one plan failed a gate assertion (EXPECT unmet, THROUGHOUT violated, NEVER tripped, SETTLE timed out)
	ExitError = 2 // could not run: cluster or /v0 unreachable, plan parse/lint error, bad arguments
)

// Suite aggregates the verdicts of one `litecone test` invocation.
type Suite struct {
	StartedAt time.Time  `json:"started_at"`
	Duration  float64    `json:"duration_seconds"`
	Verdicts  []*Verdict `json:"verdicts"`
	// Errors are plans that could not be run at all (parse error, apply
	// failure, unreachable server). They make the exit code 2.
	Errors []SuiteError `json:"errors,omitempty"`
}

// SuiteError is one plan that could not run.
type SuiteError struct {
	Plan string `json:"plan"`
	Err  string `json:"error"`
}

// Add records a verdict.
func (s *Suite) Add(v *Verdict) { s.Verdicts = append(s.Verdicts, v) }

// AddError records a plan that could not run.
func (s *Suite) AddError(plan string, err error) {
	s.Errors = append(s.Errors, SuiteError{Plan: plan, Err: err.Error()})
}

// Passed / Failed count verdicts.
func (s *Suite) Passed() int {
	n := 0
	for _, v := range s.Verdicts {
		if v.Pass {
			n++
		}
	}
	return n
}

// Failed counts failed verdicts.
func (s *Suite) Failed() int { return len(s.Verdicts) - s.Passed() }

// ExitCode applies the contract: any error -> 2, else any failure -> 1,
// else 0. An empty suite (no plans found) is an error, not a pass: a CI
// job that silently tested nothing must not go green.
func (s *Suite) ExitCode() int {
	if len(s.Errors) > 0 || len(s.Verdicts) == 0 {
		return ExitError
	}
	if s.Failed() > 0 {
		return ExitFail
	}
	return ExitPass
}

// FailSettle records a SETTLE timeout on a verdict as a failed check: the
// controller never went quiet, so the FUTURE cone is not closed and the
// run cannot be judged complete.
func FailSettle(v *Verdict, quiet, timeout time.Duration, head int64) {
	v.add(Check{
		Section: plan.SecSettle, Text: fmt.Sprintf("quiet %s within %s", quiet, timeout), Status: "fail",
		Reason: fmt.Sprintf("the timeline never went quiet for %s before the %s timeout (head stopped at revision %d)", quiet, timeout, head),
	})
}

// FailClosed adds the fail-closed guard: a plan that asserts nothing the
// gate can check is a failure, not a pass. Absence of assertions must never
// read as absence of problems.
func FailClosed(p *plan.Plan, v *Verdict) {
	machine, skipped, _, _ := p.Counts()
	if machine == 0 {
		reason := "the plan has no structural EXPECT/THROUGHOUT/NEVER assertion the gate can evaluate; add at least one (created/deleted/updated/changed with a count, or `elsewhere`), or `<kind>/<glob> ×0 changed` to assert nothing happened"
		if skipped > 0 {
			reason = fmt.Sprintf("all %d assertions are SKIPPED (field-decode / provenance pending) and nothing structural was evaluated; %s", skipped, reason)
		}
		v.add(Check{Section: plan.SecExpect, Text: "(fail-closed guard)", Status: "fail", Reason: reason})
	}
}
