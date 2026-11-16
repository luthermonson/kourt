// Package gate is litecone's deterministic oracle. It takes a parsed plan
// and a captured timeline and answers pass/fail per line, citing the events
// (ids + revisions) that decided each answer.
//
// The gate reasons about ORDER, COUNTS and OWNERSHIP-SUBTREE containment.
// It never looks at the actor (the substrate stamps every write "kine") and
// it never decodes object bodies (protobuf values are out of scope). What it
// cannot judge it marks as skipped, for the narrative judge.
//
// Light-cone vocabulary, as the checks use it:
//
//   - EXPECT     the FUTURE cone of the stimulus: effects, in causal order.
//   - THROUGHOUT invariants over the whole window: counts that must hold.
//   - NEVER      ELSEWHERE: what is causally unrelated and must not move.
//     `outside <root>` scopes "related" to the ownership subtree of root
//     (root itself plus everything whose owner chain reaches root).
package gate

import (
	"fmt"
	"path"
	"strings"

	"github.com/luthermonson/litecone/internal/plan"
	"github.com/luthermonson/litecone/internal/timeline"
)

// Evidence cites one event.
type Evidence struct {
	EventID string `json:"event_id"`
	Rev     int64  `json:"rev"`
	Seq     int64  `json:"seq,omitempty"`
	Op      string `json:"op"`
	Key     string `json:"key"`
}

func cite(e timeline.Event) Evidence {
	return Evidence{EventID: e.ID, Rev: e.Rev, Seq: e.Seq, Op: string(e.Op), Key: e.Key}
}

// Check is the verdict on one plan line.
type Check struct {
	Section  string     `json:"section"`
	LineNo   int        `json:"line"`
	Text     string     `json:"text"`
	Status   string     `json:"status"` // pass | fail | skip
	Reason   string     `json:"reason,omitempty"`
	Evidence []Evidence `json:"evidence,omitempty"`
}

// Verdict is the whole answer.
type Verdict struct {
	Plan    string   `json:"plan"`
	Pass    bool     `json:"pass"`
	Range   string   `json:"range"`
	Source  string   `json:"source"`
	Lossy   bool     `json:"lossy"`
	Events  int      `json:"events"`
	Checks  []Check  `json:"checks"`
	Passed  int      `json:"passed"`
	Failed  int      `json:"failed"`
	Skipped int      `json:"skipped"`
	Notes   []string `json:"notes,omitempty"`
}

// Judge evaluates plan p against run r.
func Judge(p *plan.Plan, r *timeline.Run) *Verdict {
	v := &Verdict{Plan: p.Name, Range: r.Range.String(), Source: r.Source, Lossy: r.Lossy, Events: len(r.Events), Pass: true}
	v.Notes = append(v.Notes, r.Notes...)
	if r.Lossy {
		v.Notes = append(v.Notes, "lossy snapshot source: only each object's create and last write are visible; counts are lower bounds and intermediate ordering is unobservable")
	}
	events := make([]timeline.Event, len(r.Events))
	copy(events, r.Events)
	timeline.Sort(events)

	for _, l := range p.Lines {
		switch {
		case l.Err != "":
			v.add(Check{Section: l.Section, LineNo: l.LineNo, Text: l.Text, Status: "fail", Reason: "does not parse: " + l.Err})
		case l.Prose:
			if l.Section == plan.SecExpect || l.Section == plan.SecThroughout || l.Section == plan.SecNever {
				v.add(Check{Section: l.Section, LineNo: l.LineNo, Text: l.Text, Status: "skip", Reason: "prose; left to the narrative judge"})
			}
		}
	}

	v.judgeExpect(p, events)
	v.judgeThroughout(p, events)
	v.judgeNever(p, events, r)
	FailClosed(p, v)
	return v
}

func (v *Verdict) add(c Check) {
	v.Checks = append(v.Checks, c)
	switch c.Status {
	case "pass":
		v.Passed++
	case "fail":
		v.Failed++
		v.Pass = false
	default:
		v.Skipped++
	}
}

// judgeExpect walks the EXPECT steps in order. Step i must be satisfied by
// events strictly after the event that satisfied step i-1 (ordering is the
// assertion). A step with xN consumes N matching events; it is satisfied at
// the N-th.
func (v *Verdict) judgeExpect(p *plan.Plan, events []timeline.Event) {
	cursor := -1 // index of the last event consumed by the previous step
	var lastEv *timeline.Event
	for _, l := range p.Steps() {
		st := l.Step
		var got []Evidence
		idx := cursor
		for i := cursor + 1; i < len(events) && len(got) < st.Count; i++ {
			if matchOp(st.Op, events[i].Op) && Match(st.Ref, events[i].Key) {
				got = append(got, cite(events[i]))
				idx = i
			}
		}
		c := Check{Section: l.Section, LineNo: l.LineNo, Text: l.Text, Evidence: got}
		if len(got) == st.Count {
			c.Status = "pass"
			if lastEv != nil {
				c.Reason = fmt.Sprintf("satisfied after %s (rev %d)", lastEv.ID, lastEv.Rev)
			}
			cursor = idx
			lastEv = &events[idx]
		} else {
			c.Status = "fail"
			after := "window start"
			if lastEv != nil {
				after = fmt.Sprintf("%s (rev %d)", lastEv.ID, lastEv.Rev)
			}
			// Was it there, but out of order?
			early := 0
			for i := 0; i <= cursor && i < len(events); i++ {
				if matchOp(st.Op, events[i].Op) && Match(st.Ref, events[i].Key) {
					early++
				}
			}
			c.Reason = fmt.Sprintf("needed %d %s event(s) matching %s after %s; found %d", st.Count, st.Op, short(st.Ref), after, len(got))
			if early > 0 {
				c.Reason += fmt.Sprintf(" (%d matching event(s) occurred BEFORE the previous step: ordering violation)", early)
			}
		}
		v.add(c)
	}
}

func (v *Verdict) judgeThroughout(p *plan.Plan, events []timeline.Event) {
	for _, l := range p.Section(plan.SecThroughout) {
		inv := l.Invariant
		if inv == nil {
			continue
		}
		var got []Evidence
		for _, e := range events {
			if matchOp(inv.Op, e.Op) && Match(inv.Ref, e.Key) {
				got = append(got, cite(e))
			}
		}
		n := len(got)
		ok := compare(n, inv.Cmp, inv.N)
		c := Check{Section: l.Section, LineNo: l.LineNo, Text: l.Text, Evidence: got}
		if ok {
			c.Status = "pass"
			c.Reason = fmt.Sprintf("%d %s event(s) matching %s; %d %s %d holds", n, inv.Op, short(inv.Ref), n, inv.Cmp, inv.N)
		} else {
			c.Status = "fail"
			c.Reason = fmt.Sprintf("%d %s event(s) matching %s; %d %s %d does not hold", n, inv.Op, short(inv.Ref), n, inv.Cmp, inv.N)
		}
		v.add(c)
	}
}

func (v *Verdict) judgeNever(p *plan.Plan, events []timeline.Event, r *timeline.Run) {
	for _, l := range p.Section(plan.SecNever) {
		nv := l.Never
		if nv == nil {
			continue
		}
		var offenders []Evidence
		switch nv.Kind {
		case "op":
			for _, e := range events {
				if matchOp(nv.Op, e.Op) && Match(nv.Ref, e.Key) {
					offenders = append(offenders, cite(e))
				}
			}
		case "outside":
			for _, e := range events {
				if !e.Op.IsObjectOp() {
					continue
				}
				if InSubtree(nv.Ref, e.Key, r.Owners[e.Key]) || matchAny(nv.Excepts, e.Key) {
					continue
				}
				offenders = append(offenders, cite(e))
			}
		}
		c := Check{Section: l.Section, LineNo: l.LineNo, Text: l.Text, Evidence: offenders}
		if len(offenders) == 0 {
			c.Status = "pass"
			if nv.Kind == "outside" {
				c.Reason = fmt.Sprintf("every object event is inside the ownership subtree of %s", short(nv.Ref))
			} else {
				c.Reason = fmt.Sprintf("no %s event matching %s", nv.Op, short(nv.Ref))
			}
		} else {
			c.Status = "fail"
			if nv.Kind == "outside" {
				c.Reason = fmt.Sprintf("%d event(s) touched objects outside the ownership subtree of %s (ELSEWHERE moved)", len(offenders), short(nv.Ref))
			} else {
				c.Reason = fmt.Sprintf("%d forbidden %s event(s) matching %s", len(offenders), nv.Op, short(nv.Ref))
			}
		}
		v.add(c)
	}
}

// InSubtree reports whether key is the root (matches the root glob) or has
// an owner in its chain that matches the root glob.
func InSubtree(rootGlob, key string, owners []string) bool {
	if Match(rootGlob, key) {
		return true
	}
	for _, o := range owners {
		if Match(rootGlob, o) {
			return true
		}
	}
	return false
}

func matchAny(globs []string, key string) bool {
	for _, g := range globs {
		if Match(g, key) {
			return true
		}
	}
	return false
}

func matchOp(want plan.Op, got timeline.Op) bool {
	switch want {
	case plan.OpAny:
		return got.IsObjectOp()
	case plan.OpCreate:
		return got == timeline.OpCreate
	case plan.OpUpdate:
		return got == timeline.OpUpdate
	case plan.OpDelete:
		return got == timeline.OpDelete
	}
	return false
}

func compare(n int, cmp string, want int) bool {
	switch cmp {
	case "==", "=":
		return n == want
	case "!=":
		return n != want
	case "<=":
		return n <= want
	case ">=":
		return n >= want
	case "<":
		return n < want
	case ">":
		return n > want
	}
	return false
}

// Match matches an etcd key against a glob. `*` matches within one
// segment (path.Match semantics); a segment that is exactly `**` matches
// zero or more whole segments.
func Match(glob, key string) bool {
	gs := strings.Split(strings.Trim(glob, "/"), "/")
	ks := strings.Split(strings.Trim(key, "/"), "/")
	return matchSegs(gs, ks)
}

func matchSegs(gs, ks []string) bool {
	for len(gs) > 0 {
		if gs[0] == "**" {
			if len(gs) == 1 {
				return true
			}
			for i := 0; i <= len(ks); i++ {
				if matchSegs(gs[1:], ks[i:]) {
					return true
				}
			}
			return false
		}
		if len(ks) == 0 {
			return false
		}
		ok, err := path.Match(gs[0], ks[0])
		if err != nil || !ok {
			return false
		}
		gs, ks = gs[1:], ks[1:]
	}
	return len(ks) == 0
}

func short(key string) string {
	return strings.TrimPrefix(key, "/registry/")
}
