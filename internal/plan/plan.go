// Package plan parses litecone plan files to the LOCKED grammar in
// PLAN-FORMAT.md. The parser is small and explicit about what it does:
//
//   - a plan is Markdown; `# PLAN <name>` names it; `## SECTION` opens one
//     of INTENT / SUBTREE / GIVEN / WHEN / EXPECT / THROUGHOUT / NEVER /
//     SETTLE / EVIDENCE (CLEANUP is an optional extension);
//   - every bullet / numbered / plain line inside a section is a line;
//   - a line that parses as a directive is MACHINE (the gate evaluates it),
//     or SKIPPED (it parses, but needs field decode or provenance the
//     substrate does not have yet), or an ERROR (it starts like a directive
//     and is malformed); anything else is PROSE for the narrative judge.
//
// Object references are `<kind>/<name-glob>`: kind is the k8s resource
// (the bead issue_type / the etcd key's resource segment), the glob matches
// the etcd key's last segment. `×N` (or `xN`) is an exact count, `×N+` at
// least N. A `→` or `->` marks a field transition.
package plan

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Section names (canonical, upper-case).
const (
	SecIntent     = "INTENT"
	SecSubtree    = "SUBTREE"
	SecGiven      = "GIVEN"
	SecWhen       = "WHEN"
	SecExpect     = "EXPECT"
	SecThroughout = "THROUGHOUT"
	SecNever      = "NEVER"
	SecSettle     = "SETTLE"
	SecEvidence   = "EVIDENCE"
	SecCleanup    = "CLEANUP" // extension: teardown applied after judging with --cleanup
)

var knownSections = map[string]bool{
	SecIntent: true, SecSubtree: true, SecGiven: true, SecWhen: true, SecExpect: true,
	SecThroughout: true, SecNever: true, SecSettle: true, SecEvidence: true, SecCleanup: true,
}

// What is the kind of assertion a matcher makes.
type What string

const (
	// Structural: evaluated by the gate from the /v0 event feed.
	Created   What = "created"
	CreatedBy What = "created-by" // created, and the owner chain reaches <ref>
	Deleted   What = "deleted"
	Updated   What = "updated" // extension: an update event
	Changed   What = "changed" // extension: any create/update/delete
	// Skipped: parse, report SKIPPED, never silently pass.
	Field      What = "field"      // <field> <old>→<new>
	Phase      What = "phase"      // phase A→B→C
	Condition  What = "condition"  // condition <X>=<val>
	StatusSet  What = "status-set" // .status.<path> set
	Provenance What = "provenance" // by: <actor>, created-by <actor>, "the ONLY actor"
)

// Structural reports whether the gate can evaluate this kind of matcher.
func (w What) Structural() bool {
	switch w {
	case Created, CreatedBy, Deleted, Updated, Changed:
		return true
	}
	return false
}

// SkipReason explains why a non-structural matcher is reported SKIPPED.
func (w What) SkipReason() string {
	switch w {
	case Provenance:
		return "provenance pending: every event's actor is \"kine\" until managedFields provenance lands"
	case Field, Phase, Condition, StatusSet:
		return "field-decode pending: object values are k8s protobuf and are not decoded yet"
	}
	return ""
}

// Ref is `<kind>/<name-glob>`.
type Ref struct {
	Kind string // glob on the resource / issue_type (e.g. machines, clusters, nodes)
	Name string // glob on the etcd key's last segment (e.g. demo-md-*)
	// Namespace is an optional middle segment (`<kind>/<ns>/<name>`), an
	// extension to the locked grammar; empty matches any namespace.
	Namespace string
}

func (r Ref) String() string {
	if r.Namespace != "" {
		return r.Kind + "/" + r.Namespace + "/" + r.Name
	}
	return r.Kind + "/" + r.Name
}

// ParseRef parses `<kind>/<name-glob>` (or `<kind>/<ns>/<name-glob>`).
func ParseRef(s string) (Ref, bool) {
	s = strings.Trim(s, "`'\",")
	parts := strings.Split(s, "/")
	switch len(parts) {
	case 2:
		if parts[0] == "" || parts[1] == "" {
			return Ref{}, false
		}
		return Ref{Kind: parts[0], Name: parts[1]}, true
	case 3:
		if parts[0] == "" || parts[2] == "" {
			return Ref{}, false
		}
		return Ref{Kind: parts[0], Namespace: parts[1], Name: parts[2]}, true
	}
	return Ref{}, false
}

// Matcher is `<kind>/<name-glob> [×N[+]] <what>`.
type Matcher struct {
	Ref     Ref
	Count   int  // exact count required, or the minimum when AtLeast
	AtLeast bool // `×N+`
	What    What
	Detail  string // the <what> text verbatim (field, phase trail, condition, actor)
	Owner   *Ref   // for CreatedBy: the owner ref
}

// Ordering keywords establish HARD ordering edges between groups/lines.
type Order struct {
	Kind   string // then | after | finally | concurrent
	Target string // group label or <kind>/<glob> for after / concurrent
}

// Step is one EXPECT matcher line.
type Step struct {
	Matcher
	Group  string  // the [group-label] in force, "" when none
	Orders []Order // ordering keywords written on this line
}

// GroupHeader is an EXPECT `[label]` line with its ordering keywords.
type GroupHeader struct {
	Label  string
	Orders []Order
}

// Action is a GIVEN / WHEN / CLEANUP input.
type Action struct {
	Verb string        // apply | patch | delete | wait
	Path string        // manifest path relative to the plan, for apply/patch/delete
	Wait time.Duration // for wait
}

// Never is one NEVER line.
type Never struct {
	Kind    string   // elsewhere | matcher
	Matcher *Matcher // for Kind == matcher (zero such events)
	Excepts []Ref    // for Kind == elsewhere
}

// Settle is one SETTLE line.
type Settle struct {
	Kind string // quiescence | timeout
	Dur  time.Duration
}

// Line is one source line of a section.
type Line struct {
	Section string
	LineNo  int
	Text    string // bullet/number prefix and trailing comment removed
	Raw     string

	Root    *Ref
	Action  *Action
	Group   *GroupHeader
	Step    *Step
	Inv     *Matcher // THROUGHOUT
	Never   *Never
	Settle  *Settle
	Prose   bool
	Skipped bool   // parses, but the gate cannot evaluate it yet
	Err     string // malformed directive
}

// Machine reports whether the gate evaluates this line structurally.
func (l *Line) Machine() bool {
	return !l.Prose && !l.Skipped && l.Err == "" && (l.Step != nil || l.Inv != nil || l.Never != nil)
}

// Plan is a parsed plan file.
type Plan struct {
	Name  string
	Path  string
	Lines []Line
}

// Section returns the lines of one section.
func (p *Plan) Section(name string) []Line {
	var out []Line
	for _, l := range p.Lines {
		if l.Section == name {
			out = append(out, l)
		}
	}
	return out
}

// Roots returns the SUBTREE ownership roots.
func (p *Plan) Roots() []Ref {
	var out []Ref
	for _, l := range p.Section(SecSubtree) {
		if l.Root != nil {
			out = append(out, *l.Root)
		}
	}
	return out
}

// Actions returns the actions of GIVEN / WHEN / CLEANUP.
func (p *Plan) Actions(section string) []Action {
	var out []Action
	for _, l := range p.Section(section) {
		if l.Action != nil {
			out = append(out, *l.Action)
		}
	}
	return out
}

// Quiescence returns the SETTLE quiescence window (default 10s) and the
// max-wait timeout (default 5m).
func (p *Plan) Quiescence() (quiet, timeout time.Duration) {
	quiet, timeout = 10*time.Second, 5*time.Minute
	for _, l := range p.Section(SecSettle) {
		if l.Settle == nil {
			continue
		}
		switch l.Settle.Kind {
		case "quiescence":
			quiet = l.Settle.Dur
		case "timeout":
			timeout = l.Settle.Dur
		}
	}
	return quiet, timeout
}

// Prose returns the prose lines of a section as text.
func (p *Plan) Prose(section string) string {
	var sb strings.Builder
	for _, l := range p.Section(section) {
		if l.Prose {
			sb.WriteString(l.Text)
			sb.WriteByte('\n')
		}
	}
	return strings.TrimSpace(sb.String())
}

// Counts summarises the plan for lint and verdicts.
func (p *Plan) Counts() (machine, skipped, prose, errs int) {
	for _, l := range p.Lines {
		switch {
		case l.Err != "":
			errs++
		case l.Skipped:
			skipped++
		case l.Prose:
			prose++
		case l.Machine():
			machine++
		}
	}
	return
}

// ParseFile parses a plan on disk.
func ParseFile(path string) (*Plan, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	p, err := Parse(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	p.Path = path
	return p, nil
}

var (
	reH1     = regexp.MustCompile(`(?i)^#\s+(?:PLAN\s+)?(\S.*)$`)
	reH2     = regexp.MustCompile(`^##+\s+(\S+)`)
	reBullet = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+`)
	reGroup  = regexp.MustCompile(`^\[([A-Za-z0-9_.:-]+)\]\s*(.*)$`)
	reCount  = regexp.MustCompile(`^[×x](\d+)(\+?)$`)
	reArrow  = regexp.MustCompile(`→|->`)
	reDur    = regexp.MustCompile(`^\d+(?:\.\d+)?(?:ns|us|µs|ms|s|m|h)$`)
)

// Parse parses plan text.
func Parse(r io.Reader) (*Plan, error) {
	p := &Plan{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	section, group := "", ""
	n := 0
	inFence := false
	for sc.Scan() {
		n++
		raw := sc.Text()
		trim := strings.TrimSpace(raw)
		if strings.HasPrefix(trim, "```") {
			inFence = !inFence
			continue
		}
		if inFence || trim == "" {
			continue
		}
		if m := reH2.FindStringSubmatch(trim); m != nil {
			section = strings.ToUpper(strings.Trim(m[1], ":"))
			group = ""
			if !knownSections[section] {
				p.Lines = append(p.Lines, Line{Section: section, LineNo: n, Raw: raw, Text: trim, Prose: true, Err: "unknown section " + section})
			}
			continue
		}
		if section == "" {
			if m := reH1.FindStringSubmatch(trim); m != nil {
				p.Name = strings.TrimSpace(m[1])
			} else if strings.HasPrefix(trim, "PLAN ") {
				p.Name = strings.TrimSpace(strings.TrimPrefix(trim, "PLAN "))
			}
			continue
		}
		text := reBullet.ReplaceAllString(trim, "")
		line := Line{Section: section, LineNo: n, Raw: raw, Text: text}
		parseLine(&line, &group)
		p.Lines = append(p.Lines, line)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if p.Name == "" {
		p.Name = "unnamed"
	}
	return p, nil
}

// stripComment removes a trailing `  # ...` or ` -- ...` comment.
func stripComment(s string) string {
	for _, sep := range []string{" # ", " -- ", "\t#"} {
		if i := strings.Index(s, sep); i >= 0 {
			s = s[:i]
		}
	}
	return strings.TrimSpace(s)
}

func parseLine(l *Line, group *string) {
	code := stripComment(l.Text)
	if code == "" || code == "(none)" || strings.HasPrefix(code, "(") && strings.HasSuffix(code, ")") {
		l.Prose = true
		return
	}
	fields := strings.Fields(code)
	switch l.Section {
	case SecSubtree:
		ref, ok := ParseRef(fields[0])
		if !ok || len(fields) != 1 {
			l.Err = fmt.Sprintf("SUBTREE lines are `<kind>/<name-glob>`, got %q", code)
			return
		}
		l.Root = &ref
	case SecGiven, SecWhen, SecCleanup:
		verb := strings.ToLower(fields[0])
		switch {
		case (verb == "apply" || verb == "patch" || verb == "delete") && len(fields) == 2:
			l.Action = &Action{Verb: verb, Path: fields[1]}
		case verb == "wait" && len(fields) == 2:
			d, err := time.ParseDuration(fields[1])
			if err != nil {
				l.Err = "bad duration " + fields[1]
				return
			}
			l.Action = &Action{Verb: "wait", Wait: d}
		default:
			l.Prose = true
		}
	case SecExpect:
		if m := reGroup.FindStringSubmatch(code); m != nil {
			*group = m[1]
			gh := &GroupHeader{Label: m[1]}
			rest, orders, err := parseOrders(m[2])
			if err != "" {
				l.Err = err
				return
			}
			if strings.TrimSpace(rest) != "" {
				l.Err = fmt.Sprintf("group header %q may only carry ordering keywords, got %q", m[1], rest)
				return
			}
			gh.Orders = orders
			l.Group = gh
			return
		}
		rest, orders, oerr := parseOrders(code)
		if oerr != "" {
			l.Err = oerr
			return
		}
		m, skipped, err := parseMatcher(rest)
		if err != "" {
			if looksLikeMatcher(rest) {
				l.Err = err
			} else {
				l.Prose = true
			}
			return
		}
		l.Step = &Step{Matcher: *m, Group: *group, Orders: orders}
		l.Skipped = skipped
	case SecThroughout:
		m, skipped, err := parseMatcher(code)
		if err != "" {
			if looksLikeMatcher(code) {
				l.Err = err
			} else {
				l.Prose = true
			}
			return
		}
		l.Inv = m
		l.Skipped = skipped
	case SecNever:
		low := strings.ToLower(code)
		if strings.HasPrefix(low, "elsewhere") || strings.HasPrefix(low, "anything elsewhere") || strings.HasPrefix(low, "nothing elsewhere") {
			nv := &Never{Kind: "elsewhere"}
			if i := strings.Index(low, "except"); i >= 0 {
				for _, tok := range strings.Fields(code[i+len("except"):]) {
					tok = strings.Trim(tok, ",")
					if tok == "" || tok == "and" {
						continue
					}
					ref, ok := ParseRef(tok)
					if !ok {
						l.Err = fmt.Sprintf("except takes `<kind>/<name-glob>` refs, got %q", tok)
						return
					}
					nv.Excepts = append(nv.Excepts, ref)
				}
			}
			l.Never = nv
			return
		}
		if strings.Contains(low, "by:") || strings.Contains(low, " actor") {
			l.Never = &Never{Kind: "matcher", Matcher: &Matcher{What: Provenance, Detail: code}}
			l.Skipped = true
			return
		}
		m, skipped, err := parseMatcher(code)
		if err != "" {
			if looksLikeMatcher(code) {
				l.Err = err
			} else {
				l.Prose = true
			}
			return
		}
		l.Never = &Never{Kind: "matcher", Matcher: m}
		l.Skipped = skipped
	case SecSettle:
		kind := strings.ToLower(fields[0])
		switch {
		case (kind == "quiescence" || kind == "quiet") && len(fields) >= 2:
			d, err := time.ParseDuration(fields[1])
			if err != nil {
				l.Err = "bad duration " + fields[1]
				return
			}
			l.Settle = &Settle{Kind: "quiescence", Dur: d}
		case (kind == "timeout" || kind == "max-wait" || kind == "max_wait") && len(fields) == 2:
			d, err := time.ParseDuration(fields[1])
			if err != nil {
				l.Err = "bad duration " + fields[1]
				return
			}
			l.Settle = &Settle{Kind: "timeout", Dur: d}
		default:
			l.Prose = true
		}
	default:
		l.Prose = true
	}
}

// parseOrders strips ordering keywords from the start and end of a line
// and returns the remainder.
func parseOrders(s string) (rest string, orders []Order, err string) {
	rest = strings.TrimSpace(s)
	for changed := true; changed; {
		changed = false
		f := strings.Fields(rest)
		if len(f) == 0 {
			break
		}
		// leading
		switch strings.ToLower(f[0]) {
		case "then":
			orders = append(orders, Order{Kind: "then"})
			rest = strings.Join(f[1:], " ")
			changed = true
			continue
		case "finally":
			orders = append(orders, Order{Kind: "finally"})
			rest = strings.Join(f[1:], " ")
			changed = true
			continue
		case "after":
			if len(f) < 2 {
				return "", nil, "`after` needs a group label or <kind>/<name-glob>"
			}
			orders = append(orders, Order{Kind: "after", Target: strings.Trim(f[1], ",")})
			rest = strings.Join(f[2:], " ")
			changed = true
			continue
		case "concurrent":
			if len(f) < 3 || strings.ToLower(f[1]) != "with" {
				return "", nil, "`concurrent with` needs a group label"
			}
			orders = append(orders, Order{Kind: "concurrent", Target: strings.Trim(f[2], ",")})
			rest = strings.Join(f[3:], " ")
			changed = true
			continue
		}
		// trailing
		n := len(f)
		switch {
		case strings.EqualFold(f[n-1], "then"), strings.EqualFold(f[n-1], "finally"):
			orders = append(orders, Order{Kind: strings.ToLower(f[n-1])})
			rest = strings.Join(f[:n-1], " ")
			changed = true
		case n >= 2 && strings.EqualFold(f[n-2], "after"):
			orders = append(orders, Order{Kind: "after", Target: f[n-1]})
			rest = strings.Join(f[:n-2], " ")
			changed = true
		case n >= 3 && strings.EqualFold(f[n-3], "concurrent") && strings.EqualFold(f[n-2], "with"):
			orders = append(orders, Order{Kind: "concurrent", Target: f[n-1]})
			rest = strings.Join(f[:n-3], " ")
			changed = true
		}
	}
	return strings.TrimSpace(rest), orders, ""
}

// looksLikeMatcher: the first token is a <kind>/<name> ref, so a parse
// failure is a malformed directive rather than prose.
func looksLikeMatcher(s string) bool {
	f := strings.Fields(s)
	if len(f) == 0 {
		return false
	}
	_, ok := ParseRef(f[0])
	return ok && !strings.Contains(f[0], ".")
}

// parseMatcher parses `<kind>/<name-glob> [×N[+]] <what>`.
func parseMatcher(s string) (m *Matcher, skipped bool, err string) {
	f := strings.Fields(s)
	if len(f) < 2 {
		return nil, false, fmt.Sprintf("expected `<kind>/<name-glob> [×N] <what>`, got %q", s)
	}
	ref, ok := ParseRef(f[0])
	if !ok {
		return nil, false, fmt.Sprintf("bad object reference %q (want <kind>/<name-glob>)", f[0])
	}
	m = &Matcher{Ref: ref, Count: 1}
	rest := f[1:]
	if c := reCount.FindStringSubmatch(rest[0]); c != nil {
		m.Count, _ = strconv.Atoi(c[1])
		m.AtLeast = c[2] == "+"
		rest = rest[1:]
		if len(rest) == 0 {
			return nil, false, fmt.Sprintf("missing <what> after the count in %q", s)
		}
	}
	what := strings.Join(rest, " ")
	m.Detail = what
	low := strings.ToLower(what)
	switch {
	case low == "created" || low == "is created" || low == "create":
		m.What = Created
	case strings.HasPrefix(low, "created-by ") || strings.HasPrefix(low, "created by "):
		tgt := strings.Fields(what)
		tgt = tgt[len(tgt)-1:]
		if o, ok := ParseRef(tgt[0]); ok {
			m.What, m.Owner = CreatedBy, &o
		} else {
			m.What = Provenance // created by <actor>
			return m, true, ""
		}
	case low == "deleted" || low == "is deleted" || low == "delete":
		m.What = Deleted
	case low == "updated" || low == "is updated" || low == "update":
		m.What = Updated
	case low == "changed" || low == "changes" || low == "touched":
		m.What = Changed
	case strings.HasPrefix(low, "phase "):
		m.What = Phase
		return m, true, ""
	case strings.HasPrefix(low, "condition "):
		m.What = Condition
		return m, true, ""
	case strings.HasPrefix(low, ".") && strings.HasSuffix(low, " set"):
		m.What = StatusSet
		return m, true, ""
	case strings.Contains(low, "by:") || strings.Contains(low, " actor"):
		m.What = Provenance
		return m, true, ""
	case reArrow.MatchString(what):
		m.What = Field
		return m, true, ""
	default:
		return nil, false, fmt.Sprintf("unknown assertion %q (created | created-by <ref> | deleted | updated | changed | <field> old→new | phase A→B | condition X=val | .status.<path> set)", what)
	}
	if m.Count < 0 {
		return nil, false, "count must be >= 0"
	}
	return m, false, ""
}
