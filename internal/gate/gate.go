// Package gate is litecone's deterministic oracle. It takes a parsed plan
// and a captured timeline and answers pass / fail / skip per line, citing
// the events (ids + revisions) that decided each answer.
//
// The gate reasons about ORDER (the partial order EXPECT declares), COUNTS
// and OWNERSHIP-SUBTREE containment. It never looks at the actor (the
// substrate stamps every write "kine") and it never decodes object bodies
// (k8s protobuf). Matchers that need either are reported SKIPPED — visible,
// never silently passed.
//
// Light-cone vocabulary, as the checks use it:
//
//   - EXPECT     the FUTURE cone of the stimulus: effects, partially ordered.
//   - THROUGHOUT invariants over the whole window: counts that must hold.
//   - NEVER      ELSEWHERE: what is causally unrelated and must not move.
//     `elsewhere` is everything not under the SUBTREE roots, computed
//     from ownerReference (parent-child) edges.
package gate

import (
	"fmt"
	"path"
	"strings"

	"github.com/luthermonson/litecone/internal/kine"
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

// Check is the verdict on one plan line (or one ordering edge).
type Check struct {
	Section  string     `json:"section"`
	LineNo   int        `json:"line"`
	Text     string     `json:"text"`
	Status   string     `json:"status"` // pass | fail | skip
	Reason   string     `json:"reason,omitempty"`
	Evidence []Evidence `json:"evidence,omitempty"`
}

// Verdict is the whole answer for one plan.
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

// Summary is the one-line human status.
func (v *Verdict) Summary() string {
	switch {
	case !v.Pass:
		return fmt.Sprintf("FAILED: %d structural check(s) failed, %d passed, %d SKIPPED", v.Failed, v.Passed, v.Skipped)
	case v.Skipped > 0:
		return fmt.Sprintf("PASSED structural checks; %d assertion(s) SKIPPED (field-decode / provenance pending)", v.Skipped)
	}
	return fmt.Sprintf("PASSED: %d structural check(s)", v.Passed)
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

	// Malformed directives fail; skipped and prose lines are reported.
	for _, l := range p.Lines {
		switch {
		case l.Err != "":
			v.add(Check{Section: l.Section, LineNo: l.LineNo, Text: l.Text, Status: "fail", Reason: "does not parse: " + l.Err})
		case l.Skipped:
			v.add(Check{Section: l.Section, LineNo: l.LineNo, Text: l.Text, Status: "skip", Reason: skipReason(l)})
		case l.Prose && (l.Section == plan.SecExpect || l.Section == plan.SecThroughout || l.Section == plan.SecNever):
			v.add(Check{Section: l.Section, LineNo: l.LineNo, Text: l.Text, Status: "skip", Reason: "prose; left to the narrative judge"})
		}
	}

	v.judgeExpect(p, events, r)
	v.judgeThroughout(p, events, r)
	v.judgeNever(p, events, r)
	FailClosed(p, v)
	return v
}

func skipReason(l plan.Line) string {
	var w plan.What
	switch {
	case l.Step != nil:
		w = l.Step.What
	case l.Inv != nil:
		w = l.Inv.What
	case l.Never != nil && l.Never.Matcher != nil:
		w = l.Never.Matcher.What
	}
	if s := w.SkipReason(); s != "" {
		return s
	}
	return "not evaluable yet"
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

// ---- matching ----------------------------------------------------------------

// MatchRef reports whether an event's object is `<kind>/<name-glob>`. Kind
// matches the key's resource segment or the bead issue_type (the kine
// driver types grouped keys by their API group, so both are tried); `nodes`
// also matches the apiserver's legacy `minions` key.
func MatchRef(ref plan.Ref, e timeline.Event) bool {
	pk := kine.ParseKey(e.Key)
	name := pk.Name
	if name == "" {
		name = e.Name
	}
	if !glob(ref.Name, name) {
		return false
	}
	if ref.Namespace != "" && !glob(ref.Namespace, pk.Namespace) {
		return false
	}
	return matchKind(ref.Kind, pk, e.Kind)
}

func matchKind(kindGlob string, pk kine.Key, issueType string) bool {
	if glob(kindGlob, pk.Resource) || glob(kindGlob, issueType) {
		return true
	}
	// services/specs, services/endpoints: the first resource segment.
	if i := strings.Index(pk.Resource, "/"); i > 0 && glob(kindGlob, pk.Resource[:i]) {
		return true
	}
	if kindGlob == "nodes" && pk.Resource == "minions" {
		return true
	}
	return false
}

// matchKeyRef matches a bare key (an owner in a chain) against a ref.
func matchKeyRef(ref plan.Ref, key string) bool {
	pk := kine.ParseKey(key)
	if !glob(ref.Name, pk.Name) {
		return false
	}
	if ref.Namespace != "" && !glob(ref.Namespace, pk.Namespace) {
		return false
	}
	return matchKind(ref.Kind, pk, "")
}

func matchOp(w plan.What, op timeline.Op) bool {
	switch w {
	case plan.Created, plan.CreatedBy:
		return op == timeline.OpCreate
	case plan.Deleted:
		return op == timeline.OpDelete
	case plan.Updated:
		return op == timeline.OpUpdate
	case plan.Changed:
		return op.IsObjectOp()
	}
	return false
}

// matches returns the events a structural matcher matches.
func matches(m *plan.Matcher, events []timeline.Event, r *timeline.Run) []timeline.Event {
	var out []timeline.Event
	for _, e := range events {
		if !matchOp(m.What, e.Op) || !MatchRef(m.Ref, e) {
			continue
		}
		if m.What == plan.CreatedBy && m.Owner != nil && !ownedBy(*m.Owner, r.Owners[e.Key]) {
			continue
		}
		out = append(out, e)
	}
	return out
}

func ownedBy(owner plan.Ref, chain []string) bool {
	for _, k := range chain {
		if matchKeyRef(owner, k) {
			return true
		}
	}
	return false
}

// InSubtree reports whether an event's object is one of the roots or has a
// root in its owner chain.
func InSubtree(roots []plan.Ref, e timeline.Event, owners []string) bool {
	for _, root := range roots {
		if MatchRef(root, e) || ownedBy(root, owners) {
			return true
		}
	}
	return false
}

// InSubtreeKey is InSubtree for a bare key (used while settling).
func InSubtreeKey(roots []plan.Ref, key string, owners []string) bool {
	for _, root := range roots {
		if matchKeyRef(root, key) || ownedBy(root, owners) {
			return true
		}
	}
	return false
}

func countOK(m *plan.Matcher, n int) bool {
	if m.AtLeast {
		return n >= m.Count
	}
	return n == m.Count
}

func countWord(m *plan.Matcher) string {
	if m.AtLeast {
		return fmt.Sprintf("at least %d", m.Count)
	}
	return fmt.Sprintf("exactly %d", m.Count)
}

// ---- EXPECT ------------------------------------------------------------------

type matched struct {
	line   plan.Line
	events []timeline.Event
}

func revSpan(evs []timeline.Event) (lo, hi int64) {
	for i, e := range evs {
		if i == 0 || e.Rev < lo {
			lo = e.Rev
		}
		if e.Rev > hi {
			hi = e.Rev
		}
	}
	return
}

// judgeExpect checks each structural matcher's count, then the hard
// ordering edges (then / after / finally / concurrent) over the matched
// events' revisions. Matchers without ordering keywords interleave freely.
func (v *Verdict) judgeExpect(p *plan.Plan, events []timeline.Event, r *timeline.Run) {
	lines := p.Section(plan.SecExpect)
	byGroup := map[string][]timeline.Event{} // structural matched events per group label
	var groupOrder []string                   // group labels in appearance order
	groupHeaders := map[string]plan.Line{}
	var steps []matched                       // structural steps in order
	stepIdx := map[int]int{}                  // line index -> steps index
	var allMatched []timeline.Event

	for i, l := range lines {
		if l.Group != nil {
			groupOrder = append(groupOrder, l.Group.Label)
			groupHeaders[l.Group.Label] = l
			if _, ok := byGroup[l.Group.Label]; !ok {
				byGroup[l.Group.Label] = nil
			}
			continue
		}
		if l.Step == nil || l.Skipped || l.Err != "" {
			continue
		}
		evs := matches(&l.Step.Matcher, events, r)
		c := Check{Section: l.Section, LineNo: l.LineNo, Text: l.Text}
		for _, e := range evs {
			c.Evidence = append(c.Evidence, cite(e))
		}
		if countOK(&l.Step.Matcher, len(evs)) {
			c.Status = "pass"
			c.Reason = fmt.Sprintf("%d %s event(s) for %s (%s required)", len(evs), l.Step.What, l.Step.Ref, countWord(&l.Step.Matcher))
		} else {
			c.Status = "fail"
			c.Reason = fmt.Sprintf("needed %s %s event(s) for %s in the window; found %d", countWord(&l.Step.Matcher), l.Step.What, l.Step.Ref, len(evs))
		}
		v.add(c)
		stepIdx[i] = len(steps)
		steps = append(steps, matched{line: l, events: evs})
		byGroup[l.Step.Group] = append(byGroup[l.Step.Group], evs...)
		allMatched = append(allMatched, evs...)
	}

	// Ordering edges on group headers.
	for gi, label := range groupOrder {
		hl := groupHeaders[label]
		for _, o := range hl.Group.Orders {
			prev := ""
			if gi > 0 {
				prev = groupOrder[gi-1]
			}
			v.add(v.edgeCheck(hl, fmt.Sprintf("[%s] %s", label, orderText(o)), byGroup[label], o, prev, byGroup, steps, allMatched, events))
		}
	}
	// Ordering edges on individual lines.
	for i, l := range lines {
		si, ok := stepIdx[i]
		if !ok || len(l.Step.Orders) == 0 {
			continue
		}
		for _, o := range l.Step.Orders {
			// `then` on a line means after the previous structural step
			// (or the previous group when this line opens a group).
			prevLabel := ""
			var prevEvents []timeline.Event
			if si > 0 {
				prevEvents = steps[si-1].events
			}
			c := v.edgeCheck(l, fmt.Sprintf("%s: %s", l.Step.Ref, orderText(o)), steps[si].events, o, prevLabel, byGroup, steps, allMatched, events)
			if o.Kind == "then" {
				c = v.thenCheck(l, fmt.Sprintf("%s: then", l.Step.Ref), steps[si].events, prevEvents, si > 0)
			}
			v.add(c)
		}
	}
}

func orderText(o plan.Order) string {
	switch o.Kind {
	case "after":
		return "after " + o.Target
	case "concurrent":
		return "concurrent with " + o.Target
	}
	return o.Kind
}

// targetEvents resolves an `after X` / `concurrent with X` target: a group
// label, else a <kind>/<glob> ref matched against every event in the window.
func targetEvents(target string, byGroup map[string][]timeline.Event, events []timeline.Event) ([]timeline.Event, bool) {
	if evs, ok := byGroup[target]; ok {
		return evs, true
	}
	if ref, ok := plan.ParseRef(target); ok {
		var out []timeline.Event
		for _, e := range events {
			if e.Op.IsObjectOp() && MatchRef(ref, e) {
				out = append(out, e)
			}
		}
		return out, true
	}
	return nil, false
}

func (v *Verdict) edgeCheck(l plan.Line, text string, mine []timeline.Event, o plan.Order, prevGroup string,
	byGroup map[string][]timeline.Event, steps []matched, allMatched, events []timeline.Event) Check {
	c := Check{Section: l.Section, LineNo: l.LineNo, Text: text}
	if len(mine) == 0 {
		c.Status = "skip"
		c.Reason = "no structural event matched on this side; ordering edge not evaluable"
		return c
	}
	lo, hi := revSpan(mine)
	switch o.Kind {
	case "then":
		if prevGroup == "" {
			c.Status = "skip"
			c.Reason = "`then` on the first group has nothing to follow"
			return c
		}
		prev := byGroup[prevGroup]
		return v.thenCheck(l, text, mine, prev, true)
	case "after":
		tgt, ok := targetEvents(o.Target, byGroup, events)
		if !ok {
			c.Status = "fail"
			c.Reason = fmt.Sprintf("unknown ordering target %q (not a group label or <kind>/<name-glob>)", o.Target)
			return c
		}
		if len(tgt) == 0 {
			c.Status = "skip"
			c.Reason = fmt.Sprintf("no event matched %s; ordering edge not evaluable", o.Target)
			return c
		}
		_, thi := revSpan(tgt)
		c.Evidence = []Evidence{cite(firstAt(mine, lo)), cite(firstAt(tgt, thi))}
		if lo > thi {
			c.Status = "pass"
			c.Reason = fmt.Sprintf("earliest rev %d is after %s's latest rev %d", lo, o.Target, thi)
		} else {
			c.Status = "fail"
			c.Reason = fmt.Sprintf("ordering violated: earliest rev %d is not after %s's latest rev %d", lo, o.Target, thi)
		}
	case "finally":
		var others []timeline.Event
		for _, e := range allMatched {
			if !containsEvent(mine, e) {
				others = append(others, e)
			}
		}
		if len(others) == 0 {
			c.Status = "skip"
			c.Reason = "nothing else matched; `finally` not evaluable"
			return c
		}
		_, ohi := revSpan(others)
		c.Evidence = []Evidence{cite(firstAt(mine, lo)), cite(firstAt(others, ohi))}
		if lo > ohi {
			c.Status = "pass"
			c.Reason = fmt.Sprintf("earliest rev %d is after every other matched event (latest %d)", lo, ohi)
		} else {
			c.Status = "fail"
			c.Reason = fmt.Sprintf("ordering violated: rev %d is not after every other matched event (latest %d)", lo, ohi)
		}
	case "concurrent":
		tgt, ok := targetEvents(o.Target, byGroup, events)
		if !ok {
			c.Status = "fail"
			c.Reason = fmt.Sprintf("unknown ordering target %q", o.Target)
			return c
		}
		if len(tgt) == 0 {
			c.Status = "skip"
			c.Reason = fmt.Sprintf("no event matched %s; overlap not evaluable", o.Target)
			return c
		}
		tlo, thi := revSpan(tgt)
		c.Evidence = []Evidence{cite(firstAt(mine, lo)), cite(firstAt(mine, hi)), cite(firstAt(tgt, tlo)), cite(firstAt(tgt, thi))}
		if lo <= thi && tlo <= hi {
			c.Status = "pass"
			c.Reason = fmt.Sprintf("revision spans overlap: [%d,%d] and %s [%d,%d]", lo, hi, o.Target, tlo, thi)
		} else {
			c.Status = "fail"
			c.Reason = fmt.Sprintf("spans do not overlap: [%d,%d] vs %s [%d,%d]", lo, hi, o.Target, tlo, thi)
		}
	default:
		c.Status = "fail"
		c.Reason = "unknown ordering keyword " + o.Kind
	}
	return c
}

func (v *Verdict) thenCheck(l plan.Line, text string, mine, prev []timeline.Event, hasPrev bool) Check {
	c := Check{Section: l.Section, LineNo: l.LineNo, Text: text}
	if !hasPrev {
		c.Status = "skip"
		c.Reason = "`then` on the first line has nothing to follow"
		return c
	}
	if len(mine) == 0 || len(prev) == 0 {
		c.Status = "skip"
		c.Reason = "no structural event matched on one side; `then` not evaluable"
		return c
	}
	lo, _ := revSpan(mine)
	_, phi := revSpan(prev)
	c.Evidence = []Evidence{cite(firstAt(prev, phi)), cite(firstAt(mine, lo))}
	if lo > phi {
		c.Status = "pass"
		c.Reason = fmt.Sprintf("earliest rev %d is after the previous group's latest rev %d", lo, phi)
	} else {
		c.Status = "fail"
		c.Reason = fmt.Sprintf("ordering violated: earliest rev %d is not after the previous group's latest rev %d", lo, phi)
	}
	return c
}

func firstAt(evs []timeline.Event, rev int64) timeline.Event {
	for _, e := range evs {
		if e.Rev == rev {
			return e
		}
	}
	return evs[0]
}

func containsEvent(evs []timeline.Event, e timeline.Event) bool {
	for _, x := range evs {
		if x.ID == e.ID {
			return true
		}
	}
	return false
}

// ---- THROUGHOUT ----------------------------------------------------------------

func (v *Verdict) judgeThroughout(p *plan.Plan, events []timeline.Event, r *timeline.Run) {
	for _, l := range p.Section(plan.SecThroughout) {
		if l.Inv == nil || l.Skipped || l.Err != "" {
			continue
		}
		evs := matches(l.Inv, events, r)
		c := Check{Section: l.Section, LineNo: l.LineNo, Text: l.Text}
		for _, e := range evs {
			c.Evidence = append(c.Evidence, cite(e))
		}
		if countOK(l.Inv, len(evs)) {
			c.Status = "pass"
			c.Reason = fmt.Sprintf("%d %s event(s) for %s across the window (%s required)", len(evs), l.Inv.What, l.Inv.Ref, countWord(l.Inv))
		} else {
			c.Status = "fail"
			c.Reason = fmt.Sprintf("invariant violated: %s %s event(s) for %s required across the window, found %d", countWord(l.Inv), l.Inv.What, l.Inv.Ref, len(evs))
		}
		v.add(c)
	}
}

// ---- NEVER ---------------------------------------------------------------------

func (v *Verdict) judgeNever(p *plan.Plan, events []timeline.Event, r *timeline.Run) {
	roots := p.Roots()
	for _, l := range p.Section(plan.SecNever) {
		if l.Never == nil || l.Skipped || l.Err != "" {
			continue
		}
		c := Check{Section: l.Section, LineNo: l.LineNo, Text: l.Text}
		var offenders []Evidence
		switch l.Never.Kind {
		case "elsewhere":
			if len(roots) == 0 {
				c.Status = "fail"
				c.Reason = "`elsewhere` needs at least one SUBTREE root"
				v.add(c)
				continue
			}
			for _, e := range events {
				if !e.Op.IsObjectOp() || InSubtree(roots, e, r.Owners[e.Key]) {
					continue
				}
				excepted := false
				for _, x := range l.Never.Excepts {
					if MatchRef(x, e) {
						excepted = true
						break
					}
				}
				if !excepted {
					offenders = append(offenders, cite(e))
				}
			}
			if len(offenders) == 0 {
				c.Status = "pass"
				c.Reason = fmt.Sprintf("every object event is inside the ownership subtree of %s", refList(roots))
			} else {
				c.Status = "fail"
				c.Reason = fmt.Sprintf("%d event(s) touched objects outside the ownership subtree of %s (ELSEWHERE moved)", len(offenders), refList(roots))
			}
		case "matcher":
			m := l.Never.Matcher
			for _, e := range matches(m, events, r) {
				offenders = append(offenders, cite(e))
			}
			if len(offenders) == 0 {
				c.Status = "pass"
				c.Reason = fmt.Sprintf("no %s event for %s", m.What, m.Ref)
			} else {
				c.Status = "fail"
				c.Reason = fmt.Sprintf("%d forbidden %s event(s) for %s", len(offenders), m.What, m.Ref)
			}
		}
		c.Evidence = offenders
		v.add(c)
	}
}

func refList(refs []plan.Ref) string {
	s := make([]string, len(refs))
	for i, r := range refs {
		s[i] = r.String()
	}
	return strings.Join(s, ", ")
}

// glob matches one path segment with path.Match semantics; `**` matches
// anything.
func glob(pattern, s string) bool {
	if pattern == "**" || pattern == "*" {
		return true
	}
	ok, err := path.Match(pattern, s)
	return err == nil && ok
}
