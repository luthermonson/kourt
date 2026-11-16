// Package plan parses litecone plan files.
//
// THE GRAMMAR IS A DRAFT (see PLAN-FORMAT.md at the repo root). The parser
// is deliberately small and permissive: a plan is Markdown; `# PLAN <name>`
// names it; each `## SECTION` heading opens a section; each bullet or
// numbered line inside a section is a line. A line that parses as a
// directive becomes machine-checkable; a line that does not is kept as
// prose for the narrative judge, and `litecone lint` reports which is which.
//
// Assumptions the parser makes (keep this list short and honest):
//
//  1. Section names are matched on their first word, case-insensitively,
//     so `## EXPECT (timeline -- order matters)` is EXPECT.
//  2. Object references are etcd-key globs without the /registry/ prefix:
//     `pods/default/podinfo-*`. `*` matches within one path segment, `**`
//     matches across segments. Namespaced vs cluster-scoped is just the
//     number of segments, exactly as the apiserver lays keys out.
//  3. Ops are create | update | delete | any.
//  4. `#` and `--` start a trailing comment on a directive line.
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
	SecGiven      = "GIVEN"
	SecWhen       = "WHEN"
	SecExpect     = "EXPECT"
	SecThroughout = "THROUGHOUT"
	SecNever      = "NEVER"
	SecSettle     = "SETTLE"
	SecEvidence   = "EVIDENCE"
	SecCleanup    = "CLEANUP"
)

var knownSections = map[string]bool{
	SecIntent: true, SecGiven: true, SecWhen: true, SecExpect: true, SecThroughout: true,
	SecNever: true, SecSettle: true, SecEvidence: true, SecCleanup: true,
}

// Op is the vocabulary of object operations a plan can name.
type Op string

const (
	OpCreate Op = "create"
	OpUpdate Op = "update"
	OpDelete Op = "delete"
	OpAny    Op = "any"
)

func parseOp(s string) (Op, bool) {
	switch strings.ToLower(s) {
	case "create", "created", "creates":
		return OpCreate, true
	case "update", "updated", "updates":
		return OpUpdate, true
	case "delete", "deleted", "deletes":
		return OpDelete, true
	case "any", "touch", "touched", "change", "changed":
		return OpAny, true
	}
	return "", false
}

// Line is one source line of a section, parsed or not.
type Line struct {
	Section string
	LineNo  int
	Text    string // the line with bullet/number prefix and trailing comment removed
	Raw     string
	// Exactly one of the following is set for a machine-checkable line.
	Action    *Action
	Step      *Step
	Invariant *Invariant
	Never     *Never
	Settle    *Settle
	// Prose is true when the line is not machine-checkable.
	Prose bool
	// Err explains why a line in a machine section did not parse (lint).
	Err string
}

// Machine reports whether the gate can judge this line.
func (l *Line) Machine() bool { return !l.Prose && l.Err == "" }

// Action is a GIVEN/WHEN/CLEANUP input: apply or delete a manifest file.
type Action struct {
	Verb string // apply | delete
	Path string // manifest path, relative to the plan file
}

// Step is one EXPECT line: `<op> <ref> [xN]`.
type Step struct {
	Op    Op
	Ref   string // normalized etcd key glob
	Count int    // exact number of matching events required (default 1)
}

// Invariant is one THROUGHOUT line.
type Invariant struct {
	Kind string // count | unchanged
	Op   Op
	Ref  string
	Cmp  string // == != <= >= < >
	N    int
}

// Never is one NEVER line.
type Never struct {
	Kind    string   // outside | op
	Op      Op       // for Kind == op
	Ref     string   // the root (outside) or the object glob (op)
	Excepts []string // allowed globs for Kind == outside
}

// Settle is one SETTLE line.
type Settle struct {
	Kind string // quiet | timeout
	Dur  time.Duration
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

// Steps returns the EXPECT directives in order.
func (p *Plan) Steps() []Line {
	var out []Line
	for _, l := range p.Section(SecExpect) {
		if l.Step != nil {
			out = append(out, l)
		}
	}
	return out
}

// Actions returns the actions of a section (GIVEN, WHEN, CLEANUP).
func (p *Plan) Actions(section string) []Action {
	var out []Action
	for _, l := range p.Section(section) {
		if l.Action != nil {
			out = append(out, *l.Action)
		}
	}
	return out
}

// Quiet returns the SETTLE quiet window (default 10s) and timeout (default 5m).
func (p *Plan) Quiet() (quiet, timeout time.Duration) {
	quiet, timeout = 10*time.Second, 5*time.Minute
	for _, l := range p.Section(SecSettle) {
		if l.Settle == nil {
			continue
		}
		switch l.Settle.Kind {
		case "quiet":
			quiet = l.Settle.Dur
		case "timeout":
			timeout = l.Settle.Dur
		}
	}
	return quiet, timeout
}

// Prose returns the non-machine lines of a section joined as text.
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
	reCount  = regexp.MustCompile(`^x(\d+)$`)
	reCmp    = regexp.MustCompile(`^(==|!=|<=|>=|<|>|=)$`)
)

// Parse parses plan text.
func Parse(r io.Reader) (*Plan, error) {
	p := &Plan{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	section := ""
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
		if inFence {
			continue
		}
		if trim == "" {
			continue
		}
		if m := reH2.FindStringSubmatch(trim); m != nil {
			section = strings.ToUpper(strings.Trim(m[1], ":"))
			if !knownSections[section] {
				p.Lines = append(p.Lines, Line{Section: section, LineNo: n, Raw: raw, Text: trim, Prose: true, Err: "unknown section"})
			}
			continue
		}
		if m := reH1.FindStringSubmatch(trim); m != nil && section == "" {
			p.Name = strings.TrimSpace(m[1])
			continue
		}
		if strings.HasPrefix(trim, "PLAN ") && section == "" {
			p.Name = strings.TrimSpace(strings.TrimPrefix(trim, "PLAN "))
			continue
		}
		if section == "" {
			continue // preamble prose
		}
		text := reBullet.ReplaceAllString(trim, "")
		line := Line{Section: section, LineNo: n, Raw: raw, Text: text}
		parseLine(&line)
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

// stripComment removes a trailing `# ...` or `-- ...` comment.
func stripComment(s string) string {
	for _, sep := range []string{" # ", " -- "} {
		if i := strings.Index(s, sep); i >= 0 {
			s = s[:i]
		}
	}
	return strings.TrimSpace(s)
}

func parseLine(l *Line) {
	code := stripComment(l.Text)
	fields := strings.Fields(code)
	if len(fields) == 0 {
		l.Prose = true
		return
	}
	switch l.Section {
	case SecGiven, SecWhen, SecCleanup:
		verb := strings.ToLower(fields[0])
		if (verb == "apply" || verb == "delete") && len(fields) == 2 {
			l.Action = &Action{Verb: verb, Path: fields[1]}
			return
		}
		l.Prose = true
	case SecExpect:
		op, ok := parseOp(fields[0])
		if !ok || len(fields) < 2 {
			l.Prose = true
			return
		}
		st := &Step{Op: op, Ref: normalizeRef(fields[1]), Count: 1}
		if len(fields) >= 3 {
			m := reCount.FindStringSubmatch(fields[2])
			if m == nil || len(fields) > 3 {
				l.Err = fmt.Sprintf("expected `<op> <ref> [xN]`, got %q", code)
				return
			}
			st.Count, _ = strconv.Atoi(m[1])
			if st.Count < 1 {
				l.Err = "count must be >= 1"
				return
			}
		}
		l.Step = st
	case SecThroughout:
		switch strings.ToLower(fields[0]) {
		case "count":
			// count <op> <ref> <cmp> <n>
			if len(fields) != 5 {
				l.Err = fmt.Sprintf("expected `count <op> <ref> <cmp> <n>`, got %q", code)
				return
			}
			op, ok := parseOp(fields[1])
			if !ok {
				l.Err = "unknown op " + fields[1]
				return
			}
			if !reCmp.MatchString(fields[3]) {
				l.Err = "unknown comparison " + fields[3]
				return
			}
			nval, err := strconv.Atoi(fields[4])
			if err != nil {
				l.Err = "count needs an integer, got " + fields[4]
				return
			}
			cmp := fields[3]
			if cmp == "=" {
				cmp = "=="
			}
			l.Invariant = &Invariant{Kind: "count", Op: op, Ref: normalizeRef(fields[2]), Cmp: cmp, N: nval}
		case "unchanged":
			if len(fields) != 2 {
				l.Err = fmt.Sprintf("expected `unchanged <ref>`, got %q", code)
				return
			}
			l.Invariant = &Invariant{Kind: "unchanged", Op: OpAny, Ref: normalizeRef(fields[1]), Cmp: "==", N: 0}
		default:
			l.Prose = true
		}
	case SecNever:
		if strings.ToLower(fields[0]) == "outside" {
			if len(fields) < 2 {
				l.Err = "expected `outside <root-ref> [except <ref> ...]`"
				return
			}
			nv := &Never{Kind: "outside", Ref: normalizeRef(fields[1])}
			rest := fields[2:]
			if len(rest) > 0 {
				if strings.ToLower(rest[0]) != "except" || len(rest) < 2 {
					l.Err = "expected `except <ref> ...` after the root"
					return
				}
				for _, r := range rest[1:] {
					nv.Excepts = append(nv.Excepts, normalizeRef(strings.TrimSuffix(r, ",")))
				}
			}
			l.Never = nv
			return
		}
		if op, ok := parseOp(fields[0]); ok && len(fields) == 2 {
			l.Never = &Never{Kind: "op", Op: op, Ref: normalizeRef(fields[1])}
			return
		}
		l.Prose = true
	case SecSettle:
		kind := strings.ToLower(fields[0])
		if (kind == "quiet" || kind == "timeout") && len(fields) == 2 {
			d, err := time.ParseDuration(fields[1])
			if err != nil {
				l.Err = "bad duration " + fields[1]
				return
			}
			l.Settle = &Settle{Kind: kind, Dur: d}
			return
		}
		l.Prose = true
	default:
		l.Prose = true
	}
}

// normalizeRef turns a plan reference into the /registry/... glob form.
func normalizeRef(ref string) string {
	ref = strings.Trim(ref, "`'\"")
	if strings.HasPrefix(ref, "/registry/") {
		return ref
	}
	return "/registry/" + strings.TrimPrefix(ref, "/")
}
