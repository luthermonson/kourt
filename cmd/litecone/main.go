// Command litecone is the causality-first test oracle for Kubernetes
// controllers running on k3s-on-beads. See README.md.
//
// Exit codes (the CI contract, see internal/gate/suite.go):
//
//	0  every plan passed
//	1  one or more plans failed a gate assertion
//	2  could not run (server unreachable, parse/lint error, bad arguments)
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/luthermonson/litecone/internal/beads"
	"github.com/luthermonson/litecone/internal/gate"
	"github.com/luthermonson/litecone/internal/k8s"
	"github.com/luthermonson/litecone/internal/kine"
	"github.com/luthermonson/litecone/internal/plan"
	"github.com/luthermonson/litecone/internal/report"
	"github.com/luthermonson/litecone/internal/timeline"
)

// version is set by the Makefile / goreleaser via -ldflags.
var version = "dev"

const (
	flagBeads      = "beads"
	flagKubeconfig = "kubeconfig"
	flagSource     = "source"
	flagJSON       = "json"
	flagQuiet      = "quiet"
	flagToken      = "token"
	flagReport     = "report"
	flagReportPath = "report-path"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

// run is the testable entry point. It returns the process exit code.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	app := newApp(stdout, stderr)
	args = reorderArgs(app, args)
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") && app.Command(args[0]) == nil && args[0] != "help" && args[0] != "h" {
		fmt.Fprintf(stderr, "litecone: unknown command %q (see `litecone --help`)\n", args[0])
		return gate.ExitError
	}
	err := app.RunContext(ctx, append([]string{"litecone"}, args...))
	if err == nil {
		return gate.ExitPass
	}
	var ec cli.ExitCoder
	if errors.As(err, &ec) {
		if msg := ec.Error(); msg != "" {
			fmt.Fprintln(stderr, "litecone:", msg)
		}
		return ec.ExitCode()
	}
	fmt.Fprintln(stderr, "litecone:", err)
	return gate.ExitError
}

func newApp(stdout, stderr io.Writer) *cli.App {
	app := &cli.App{
		Name:    "litecone",
		Usage:   "causality-first testing for Kubernetes controllers on k3s-on-beads",
		Version: version,
		Description: `litecone judges the committed, ordered, revision-stamped timeline of what a
controller actually did, as recorded in the beads work-graph behind kine,
instead of polling live cluster state. An event's PAST cone is its causes,
its FUTURE cone its effects, and ELSEWHERE is what must not have moved.

Exit codes: 0 all plans passed; 1 a gate assertion failed; 2 could not run.`,
		Writer:    stdout,
		ErrWriter: stderr,
		// urfave prints usage errors itself; we still want exit 2 for them.
		OnUsageError: func(c *cli.Context, err error, _ bool) error {
			return cli.Exit(err.Error(), gate.ExitError)
		},
		ExitErrHandler: func(*cli.Context, error) {}, // never os.Exit from inside the library
		Flags: []cli.Flag{
			&cli.StringFlag{Name: flagBeads, Usage: "beads /v0 base URL", Value: "http://127.0.0.1:8099", EnvVars: []string{"LITECONE_BEADS"}},
			&cli.StringFlag{Name: flagToken, Usage: "bearer token for bd serve --auth-token-file", EnvVars: []string{"LITECONE_BEADS_TOKEN"}},
			&cli.StringFlag{Name: flagKubeconfig, Usage: "kubeconfig path (default: $KUBECONFIG, ~/.kube/config)", EnvVars: []string{"LITECONE_KUBECONFIG"}},
			&cli.StringFlag{Name: flagSource, Usage: "timeline source: auto|journal|snapshot", Value: "auto", EnvVars: []string{"LITECONE_SOURCE"}},
			&cli.BoolFlag{Name: flagJSON, Usage: "machine-readable output"},
			&cli.BoolFlag{Name: flagQuiet, Aliases: []string{"q"}, Usage: "suppress progress on stderr"},
		},
		Commands: []*cli.Command{
			cmdTest(), cmdReplay(), cmdLint(), cmdDiff(), cmdRecord(), cmdRev(), cmdInfo(),
		},
	}
	return app
}

// reorderArgs lets flags follow positionals (`litecone replay plan.md --run
// x.json`). urfave/cli v2 stops flag parsing at the first positional, so the
// sub-command's flags are moved in front of its arguments here. Bool flags
// take no value; everything else consumes the next token unless written as
// --flag=value.
func reorderArgs(app *cli.App, args []string) []string {
	// Split off global flags up to the command name.
	i := 0
	isBool := boolFlagSet(app.Flags)
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		name := strings.TrimLeft(args[i], "-")
		if !strings.Contains(args[i], "=") && !isBool[name] {
			i++
		}
		i++
	}
	if i >= len(args) {
		return args
	}
	cmd := app.Command(args[i])
	if cmd == nil {
		return args
	}
	cmdBool := boolFlagSet(append(append([]cli.Flag{}, cmd.Flags...), app.Flags...))
	head := append([]string{}, args[:i+1]...)
	var flags, pos []string
	rest := args[i+1:]
	for j := 0; j < len(rest); j++ {
		a := rest[j]
		if a == "--" {
			pos = append(pos, rest[j+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") {
			pos = append(pos, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if !strings.Contains(a, "=") && !cmdBool[name] && j+1 < len(rest) {
			j++
			flags = append(flags, rest[j])
		}
	}
	return append(append(head, flags...), pos...)
}

func boolFlagSet(flags []cli.Flag) map[string]bool {
	m := map[string]bool{"help": true, "h": true, "version": true, "v": true}
	for _, f := range flags {
		if _, ok := f.(*cli.BoolFlag); ok {
			for _, n := range f.Names() {
				m[n] = true
			}
		}
	}
	return m
}

func out(c *cli.Context) io.Writer { return c.App.Writer }

func capturer(c *cli.Context) *timeline.Capturer {
	cl := beads.New(c.String(flagBeads))
	cl.Token = c.String(flagToken)
	cap := &timeline.Capturer{Client: cl}
	if !c.Bool(flagQuiet) {
		w := c.App.ErrWriter
		cap.Logf = func(f string, a ...any) { fmt.Fprintf(w, "litecone: "+f+"\n", a...) }
	}
	return cap
}

func logger(c *cli.Context) func(string, ...any) {
	if c.Bool(flagQuiet) {
		return func(string, ...any) {}
	}
	w := c.App.ErrWriter
	return func(f string, a ...any) { fmt.Fprintf(w, "litecone: "+f+"\n", a...) }
}

func source(c *cli.Context) (timeline.Source, error) {
	return timeline.ParseSource(c.String(flagSource))
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func exitErr(err error) error { return cli.Exit(err.Error(), gate.ExitError) }

// parseRange accepts "A..B", "A.." and "A..head".
func parseRange(s string) (timeline.Range, error) {
	s = strings.TrimSpace(s)
	parts := strings.SplitN(s, "..", 2)
	if len(parts) != 2 {
		return timeline.Range{}, fmt.Errorf("range must look like A..B (got %q)", s)
	}
	from, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return timeline.Range{}, fmt.Errorf("bad start revision %q", parts[0])
	}
	var to int64
	if parts[1] != "" && parts[1] != "head" {
		to, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return timeline.Range{}, fmt.Errorf("bad end revision %q", parts[1])
		}
		if to < from {
			return timeline.Range{}, fmt.Errorf("end revision %d is before start %d", to, from)
		}
	}
	return timeline.Range{From: from, To: to}, nil
}

// reportFlags are shared by test and replay.
func reportFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: flagReport, Usage: "report format: junit (one <testcase> per plan)"},
		&cli.StringFlag{Name: flagReportPath, Usage: "where to write the report", Value: "litecone-report.xml"},
	}
}

func writeReport(c *cli.Context, s *gate.Suite) error {
	switch c.String(flagReport) {
	case "":
		return nil
	case "junit":
		return report.WriteJUnitFile(c.String(flagReportPath), s)
	}
	return fmt.Errorf("unknown report format %q (junit)", c.String(flagReport))
}

// finish prints the suite summary, writes reports and maps to the exit code.
func finish(c *cli.Context, s *gate.Suite) error {
	s.Duration = time.Since(s.StartedAt).Seconds()
	if err := writeReport(c, s); err != nil {
		return exitErr(err)
	}
	if c.Bool(flagJSON) {
		_ = writeJSON(out(c), s)
	} else {
		w := out(c)
		for _, v := range s.Verdicts {
			printVerdict(w, v)
		}
		for _, e := range s.Errors {
			fmt.Fprintf(w, "ERROR plan=%s: %s\n", e.Plan, e.Err)
		}
		fmt.Fprintf(w, "suite: %d plan(s), %d passed, %d failed, %d error(s)  exit=%d\n",
			len(s.Verdicts)+len(s.Errors), s.Passed(), s.Failed(), len(s.Errors), s.ExitCode())
	}
	if code := s.ExitCode(); code != 0 {
		return cli.Exit("", code)
	}
	return nil
}

// planFiles expands file-or-directory arguments into plan paths. A directory
// contributes every *.plan.md and *.md file directly inside it, sorted.
func planFiles(args []string) ([]string, error) {
	var files []string
	for _, a := range args {
		st, err := os.Stat(a)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() {
			files = append(files, a)
			continue
		}
		ents, err := os.ReadDir(a)
		if err != nil {
			return nil, err
		}
		var found []string
		for _, e := range ents {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || strings.EqualFold(e.Name(), "README.md") {
				continue
			}
			found = append(found, filepath.Join(a, e.Name()))
		}
		sort.Strings(found)
		files = append(files, found...)
	}
	if len(files) == 0 {
		return nil, errors.New("no plan files found")
	}
	return files, nil
}

// ---- info / rev ------------------------------------------------------------

func cmdInfo() *cli.Command {
	return &cli.Command{
		Name:  "info",
		Usage: "handshake with the beads server and report what litecone can use",
		Action: func(c *cli.Context) error {
			cap := capturer(c)
			bc, err := cap.Client.Context(c.Context)
			if err != nil {
				return exitErr(err)
			}
			journal, jerr := cap.Client.JournalEnabled(c.Context)
			head, err := cap.Head(c.Context)
			if err != nil {
				return exitErr(err)
			}
			res := map[string]any{
				"server": cap.Client.BaseURL, "bd_version": bc.BDVersion, "backend": bc.Backend, "dolt_mode": bc.DoltMode,
				"wire_revision": bc.WireRevision, "events_list": bc.Has("events.list"), "dependencies_tree": bc.Has("dependencies.tree"),
				"journal_enabled": journal, "head_revision": head,
			}
			if jerr != nil {
				res["journal_error"] = jerr.Error()
			}
			if c.Bool(flagJSON) {
				return writeJSON(out(c), res)
			}
			w := out(c)
			fmt.Fprintf(w, "server            %s\n", cap.Client.BaseURL)
			fmt.Fprintf(w, "bd                %s (%s, dolt %s, wire %d)\n", bc.BDVersion, bc.Backend, bc.DoltMode, bc.WireRevision)
			fmt.Fprintf(w, "capabilities      events.list=%v dependencies.tree=%v\n", bc.Has("events.list"), bc.Has("dependencies.tree"))
			fmt.Fprintf(w, "journal enabled   %v\n", journal)
			if !journal {
				fmt.Fprintln(w, "                  -> timelines fall back to the LOSSY snapshot source; enable with")
				fmt.Fprintln(w, "                     `bd config set events-journal true` on the workspace, then restart kine-beads and bd serve")
			}
			fmt.Fprintf(w, "head revision     %d\n", head)
			return nil
		},
	}
}

func cmdRev() *cli.Command {
	return &cli.Command{
		Name:  "rev",
		Usage: "print the current head kine revision (max modRev over all k8s beads)",
		Action: func(c *cli.Context) error {
			head, err := capturer(c).Head(c.Context)
			if err != nil {
				return exitErr(err)
			}
			if c.Bool(flagJSON) {
				return writeJSON(out(c), map[string]int64{"head": head})
			}
			fmt.Fprintln(out(c), head)
			return nil
		},
	}
}

// ---- diff ------------------------------------------------------------------

func cmdDiff() *cli.Command {
	return &cli.Command{
		Name:      "diff",
		Usage:     "full cluster changeset between two kine revisions (A exclusive, B inclusive; B may be `head`)",
		ArgsUsage: "<revA> <revB>",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "events", Usage: "list every event, not just one row per object"},
		},
		Action: func(c *cli.Context) error {
			if c.NArg() != 2 {
				return cli.Exit("usage: litecone diff <revA> <revB>", gate.ExitError)
			}
			rng, err := parseRange(c.Args().Get(0) + ".." + c.Args().Get(1))
			if err != nil {
				return exitErr(err)
			}
			src, err := source(c)
			if err != nil {
				return exitErr(err)
			}
			run, err := capturer(c).Capture(c.Context, rng, src)
			if err != nil {
				return exitErr(err)
			}
			cs := timeline.Diff(run)
			if c.Bool(flagJSON) {
				if c.Bool("events") {
					return writeJSON(out(c), run)
				}
				return writeJSON(out(c), cs)
			}
			printChangeset(out(c), run, cs, c.Bool("events"))
			return nil
		},
	}
}

func printChangeset(w io.Writer, run *timeline.Run, cs *timeline.Changeset, events bool) {
	fmt.Fprintf(w, "range %s  source=%s lossy=%v  objects=%d events=%d (create %d, update %d, delete %d)\n",
		run.Range, cs.Source, cs.Lossy, cs.Totals.Objects, cs.Totals.Events, cs.Totals.Creates, cs.Totals.Updates, cs.Totals.Deletes)
	for _, n := range cs.Notes {
		fmt.Fprintf(w, "note: %s\n", n)
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if events {
		fmt.Fprintln(tw, "REV\tID\tOP\tKIND\tKEY\tOWNER")
		for _, e := range run.Events {
			owner := ""
			if o := run.Owners[e.Key]; len(o) > 0 {
				owner = kine.Short(o[0])
			}
			if e.Owner != "" {
				owner = kine.Short(e.Owner)
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n", e.Rev, e.ID, e.Op, e.Kind, kine.Short(e.Key), owner)
		}
	} else {
		fmt.Fprintln(tw, "CHANGE\tKIND\tKEY\tREVS\tEVENTS\tOWNER")
		for _, o := range cs.Objects {
			owner := ""
			if len(o.Owners) > 0 {
				owner = kine.Short(o.Owners[0])
			}
			revs := strconv.FormatInt(o.FirstRev, 10)
			if o.LastRev != o.FirstRev {
				revs += "-" + strconv.FormatInt(o.LastRev, 10)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\n", o.Change, o.Kind, kine.Short(o.Key), revs, o.Events, owner)
		}
	}
	tw.Flush()
}

// ---- record ----------------------------------------------------------------

func cmdRecord() *cli.Command {
	return &cli.Command{
		Name:  "record",
		Usage: "capture an event range to a run file for offline replay",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "range", Aliases: []string{"r"}, Usage: "revision window A..B (B may be `head` or empty)", Required: true},
			&cli.StringFlag{Name: "out", Aliases: []string{"o"}, Usage: "output file", Value: "run.json"},
			&cli.StringFlag{Name: "plan", Usage: "plan path to note in the run file"},
		},
		Action: func(c *cli.Context) error {
			rng, err := parseRange(c.String("range"))
			if err != nil {
				return exitErr(err)
			}
			src, err := source(c)
			if err != nil {
				return exitErr(err)
			}
			run, err := capturer(c).Capture(c.Context, rng, src)
			if err != nil {
				return exitErr(err)
			}
			run.Plan = c.String("plan")
			if err := run.Save(c.String("out")); err != nil {
				return exitErr(err)
			}
			if c.Bool(flagJSON) {
				return writeJSON(out(c), map[string]any{"out": c.String("out"), "range": run.Range, "events": len(run.Events), "source": run.Source, "lossy": run.Lossy})
			}
			fmt.Fprintf(out(c), "recorded %d events for %s (source=%s lossy=%v) -> %s\n", len(run.Events), run.Range, run.Source, run.Lossy, c.String("out"))
			return nil
		},
	}
}

// ---- lint ------------------------------------------------------------------

type lintRow struct {
	Line    int    `json:"line,omitempty"`
	Section string `json:"section,omitempty"`
	Kind    string `json:"kind"` // machine | prose | error
	Text    string `json:"text,omitempty"`
	Err     string `json:"err,omitempty"`
}

// lintPlan returns the rows and the number of errors. A plan with no
// machine-checkable assertion is an error (fail-closed).
func lintPlan(p *plan.Plan) (rows []lintRow, machine, prose, errs int) {
	for _, l := range p.Lines {
		r := lintRow{Line: l.LineNo, Section: l.Section, Text: l.Text, Err: l.Err}
		switch {
		case l.Err != "":
			r.Kind = "error"
			errs++
		case l.Skipped:
			r.Kind = "skipped"
		case l.Prose:
			r.Kind = "prose"
			prose++
		default:
			r.Kind = "machine"
			if l.Machine() {
				machine++
			}
		}
		rows = append(rows, r)
	}
	if machine == 0 {
		errs++
		rows = append(rows, lintRow{Kind: "error", Err: "plan has no structural EXPECT/THROUGHOUT/NEVER assertion the gate can evaluate (fail-closed)"})
	}
	if len(p.Roots()) == 0 {
		for _, l := range p.Section(plan.SecNever) {
			if l.Never != nil && l.Never.Kind == "elsewhere" {
				errs++
				rows = append(rows, lintRow{Kind: "error", Line: l.LineNo, Section: l.Section, Err: "`elsewhere` needs a SUBTREE section with at least one root"})
			}
		}
	}
	for _, sec := range []string{plan.SecGiven, plan.SecWhen, plan.SecCleanup} {
		for _, a := range p.Actions(sec) {
			path := a.Path
			if !filepath.IsAbs(path) {
				path = filepath.Join(filepath.Dir(p.Path), path)
			}
			if _, err := os.Stat(path); err != nil {
				errs++
				rows = append(rows, lintRow{Kind: "error", Section: sec, Err: fmt.Sprintf("%s manifest not found: %s", a.Verb, path)})
			}
		}
	}
	return rows, machine, prose, errs
}

func cmdLint() *cli.Command {
	return &cli.Command{
		Name:      "lint",
		Usage:     "validate plans: which lines the gate can judge, which are prose, which do not parse",
		ArgsUsage: "<plan.md|dir> ...",
		Action: func(c *cli.Context) error {
			if c.NArg() < 1 {
				return cli.Exit("usage: litecone lint <plan.md|dir> ...", gate.ExitError)
			}
			files, err := planFiles(c.Args().Slice())
			if err != nil {
				return exitErr(err)
			}
			total := 0
			var all []map[string]any
			for _, f := range files {
				p, err := plan.ParseFile(f)
				if err != nil {
					total++
					all = append(all, map[string]any{"plan": f, "ok": false, "error": err.Error()})
					if !c.Bool(flagJSON) {
						fmt.Fprintf(out(c), "%s: %v\n", f, err)
					}
					continue
				}
				rows, machine, prose, errs := lintPlan(p)
				total += errs
				all = append(all, map[string]any{"plan": p.Name, "path": f, "ok": errs == 0, "machine": machine, "prose": prose, "errors": errs, "lines": rows})
				if c.Bool(flagJSON) {
					continue
				}
				w := out(c)
				fmt.Fprintf(w, "%s (%s): %d machine-checkable, %d prose (narrative judge), %d error(s)\n", p.Name, f, machine, prose, errs)
				for _, r := range rows {
					if r.Kind == "machine" {
						continue
					}
					if r.Line > 0 {
						fmt.Fprintf(w, "  %-6s L%-4d %-10s %s", r.Kind, r.Line, r.Section, r.Text)
					} else {
						fmt.Fprintf(w, "  %-6s ", r.Kind)
					}
					if r.Err != "" {
						fmt.Fprintf(w, "  <- %s", r.Err)
					}
					fmt.Fprintln(w)
				}
			}
			if c.Bool(flagJSON) {
				_ = writeJSON(out(c), map[string]any{"ok": total == 0, "errors": total, "plans": all})
			}
			if total > 0 {
				return cli.Exit("", gate.ExitError)
			}
			return nil
		},
	}
}

// ---- replay ----------------------------------------------------------------

func cmdReplay() *cli.Command {
	return &cli.Command{
		Name:      "replay",
		Usage:     "re-judge a plan against a committed range, offline (run file) or by re-reading the server",
		ArgsUsage: "<plan.md>",
		Flags: append([]cli.Flag{
			&cli.StringFlag{Name: "run", Usage: "run file written by `litecone record` or `litecone test` (offline; no cluster, no beads)"},
			&cli.StringFlag{Name: "range", Aliases: []string{"r"}, Usage: "revision window A..B to re-read from the beads server"},
			&cli.StringFlag{Name: "out", Aliases: []string{"o"}, Usage: "write the verdict JSON here as well"},
		}, reportFlags()...),
		Action: func(c *cli.Context) error {
			if c.NArg() != 1 {
				return cli.Exit("usage: litecone replay <plan.md> (--run run.json | --range A..B)", gate.ExitError)
			}
			p, err := plan.ParseFile(c.Args().First())
			if err != nil {
				return exitErr(err)
			}
			if _, _, _, errs := lintPlan(p); errs > 0 {
				// Manifest-existence errors do not matter offline; only directive errors do.
				for _, l := range p.Lines {
					if l.Err != "" {
						return exitErr(fmt.Errorf("%s L%d: %s", p.Path, l.LineNo, l.Err))
					}
				}
			}
			var run *timeline.Run
			switch {
			case c.String("run") != "":
				run, err = timeline.Load(c.String("run"))
			case c.String("range") != "":
				var rng timeline.Range
				rng, err = parseRange(c.String("range"))
				if err == nil {
					var src timeline.Source
					src, err = source(c)
					if err == nil {
						run, err = capturer(c).Capture(c.Context, rng, src)
					}
				}
			default:
				return cli.Exit("replay needs --run <run.json> or --range A..B", gate.ExitError)
			}
			if err != nil {
				return exitErr(err)
			}
			s := &gate.Suite{StartedAt: time.Now()}
			v := gate.Judge(p, run)
			s.Add(v)
			if c.String("out") != "" {
				if f, err := os.Create(c.String("out")); err == nil {
					_ = writeJSON(f, v)
					f.Close()
				}
			}
			return finish(c, s)
		},
	}
}

// ---- test ------------------------------------------------------------------

func cmdTest() *cli.Command {
	return &cli.Command{
		Name:      "test",
		Usage:     "apply each plan's inputs, capture the resulting revision range, run the gate; exit 1 if ANY plan fails",
		ArgsUsage: "<plan.md|dir> ...",
		Flags: append([]cli.Flag{
			&cli.StringFlag{Name: "out-dir", Usage: "where to write <plan>.run.json and <plan>.verdict.json", Value: ".litecone"},
			&cli.BoolFlag{Name: "skip-given", Usage: "do not apply GIVEN (preconditions already present)"},
			&cli.BoolFlag{Name: "cleanup", Usage: "apply each plan's CLEANUP section after judging"},
		}, reportFlags()...),
		Description: `Per plan:
  1. apply GIVEN manifests, settle (quiet window from SETTLE)
  2. A = head revision
  3. apply WHEN manifests
  4. settle: poll the head revision until it is unchanged for the quiet window;
     hitting the SETTLE timeout is a gate FAILURE (the controller never went quiet)
  5. B = head revision; capture (A, B]; judge; write run + verdict files
Plans run sequentially. A plan that cannot run (parse error, apply error,
unreachable server) is an ERROR and makes the exit code 2.`,
		Action: func(c *cli.Context) error {
			if c.NArg() < 1 {
				return cli.Exit("usage: litecone test <plan.md|dir> ...", gate.ExitError)
			}
			files, err := planFiles(c.Args().Slice())
			if err != nil {
				return exitErr(err)
			}
			src, err := source(c)
			if err != nil {
				return exitErr(err)
			}
			s := &gate.Suite{StartedAt: time.Now()}
			logf := logger(c)

			// Parse and lint everything first: a broken plan is an error for
			// the whole invocation before any input touches the cluster.
			var plans []*plan.Plan
			for _, f := range files {
				p, err := plan.ParseFile(f)
				if err != nil {
					s.AddError(f, err)
					continue
				}
				if rows, _, _, errs := lintPlan(p); errs > 0 {
					var msgs []string
					for _, r := range rows {
						if r.Kind == "error" {
							msgs = append(msgs, r.Err)
						}
					}
					s.AddError(f, fmt.Errorf("lint: %s", strings.Join(msgs, "; ")))
					continue
				}
				plans = append(plans, p)
			}
			if len(s.Errors) > 0 {
				return finish(c, s)
			}

			cap := capturer(c)
			if _, err := cap.Client.Context(c.Context); err != nil {
				return exitErr(fmt.Errorf("beads /v0 unreachable: %w", err))
			}
			kc, err := k8s.Connect(c.String(flagKubeconfig))
			if err != nil {
				return exitErr(err)
			}
			if err := os.MkdirAll(c.String("out-dir"), 0o755); err != nil {
				return exitErr(err)
			}

			for _, p := range plans {
				v, err := runPlan(c.Context, p, cap, kc, src, c.Bool("skip-given"), c.Bool("cleanup"), c.String("out-dir"), logf)
				if err != nil {
					s.AddError(p.Path, err)
					continue
				}
				s.Add(v)
			}
			return finish(c, s)
		},
	}
}

// runPlan executes one plan end to end and returns its verdict. An error
// means the plan could not be run (not a gate failure).
func runPlan(ctx context.Context, p *plan.Plan, cap *timeline.Capturer, kc *k8s.Client, src timeline.Source,
	skipGiven, cleanup bool, outDir string, logf func(string, ...any)) (*gate.Verdict, error) {
	quiet, timeout := p.Quiescence()
	dir := filepath.Dir(p.Path)
	runActions := func(section string) error {
		for _, a := range p.Actions(section) {
			path := a.Path
			if !filepath.IsAbs(path) {
				path = filepath.Join(dir, path)
			}
			var objs []k8s.Object
			var err error
			if a.Verb == "apply" {
				objs, err = kc.Apply(ctx, path)
			} else {
				objs, err = kc.Delete(ctx, path)
			}
			if err != nil {
				return fmt.Errorf("%s %s: %w", section, a.Verb, err)
			}
			for _, o := range objs {
				logf("[%s] %s: %s %s (%s)", p.Name, section, a.Verb, kine.Short(o.Key), o.GVK.Kind)
			}
		}
		return nil
	}

	// Quiescence is judged ON THE SUBTREE: a write counts as activity only
	// when its object is a SUBTREE root or has one in its owner chain. The
	// global revision never stops moving on a live cluster (node leases renew
	// every few seconds), so this is the only meaningful settle signal. The
	// owner chain of each bead is resolved once and cached.
	roots := p.Roots()
	chains := map[string][]string{}
	keep := func(is *beads.Issue) bool {
		if len(roots) == 0 {
			return true // no SUBTREE: fall back to the global head
		}
		chain, ok := chains[is.ID]
		if !ok {
			var err error
			chain, err = cap.OwnerChain(ctx, is.ID, 16)
			if err != nil {
				chain = nil
			}
			chains[is.ID] = chain
		}
		return gate.InSubtreeKey(roots, is.Key(), chain)
	}
	logf("[%s] start (quiescence on subtree %s)", p.Name, refList(roots))
	if !skipGiven && len(p.Actions(plan.SecGiven)) > 0 {
		if err := runActions(plan.SecGiven); err != nil {
			return nil, err
		}
		if _, _, err := settle(ctx, cap, keep, quiet, timeout, logf); err != nil {
			return nil, err
		}
	}
	from, err := cap.Head(ctx)
	if err != nil {
		return nil, err
	}
	logf("[%s] A = %d", p.Name, from)
	if err := runActions(plan.SecWhen); err != nil {
		return nil, err
	}
	_, timedOut, err := settle(ctx, cap, keep, quiet, timeout, logf)
	if err != nil {
		return nil, err
	}
	// B is the unfiltered head: the window must contain every write, so
	// NEVER can see background keys the plan did NOT except.
	to, err := cap.Head(ctx)
	if err != nil {
		return nil, err
	}
	logf("[%s] B = %d", p.Name, to)
	run, err := cap.Capture(ctx, timeline.Range{From: from, To: to}, src)
	if err != nil {
		return nil, err
	}
	run.Plan = p.Path
	base := filepath.Join(outDir, safeName(p.Name))
	if err := run.Save(base + ".run.json"); err != nil {
		return nil, err
	}
	logf("[%s] run file: %s (%d events)", p.Name, base+".run.json", len(run.Events))
	v := gate.Judge(p, run)
	if timedOut {
		gate.FailSettle(v, quiet, timeout, to)
	}
	if cleanup {
		if err := runActions(plan.SecCleanup); err != nil {
			v.Notes = append(v.Notes, "cleanup failed: "+err.Error())
		}
	}
	if f, err := os.Create(base + ".verdict.json"); err == nil {
		_ = writeJSON(f, v)
		f.Close()
	}
	return v, nil
}

func refList(refs []plan.Ref) string {
	if len(refs) == 0 {
		return "(none: global head)"
	}
	s := make([]string, len(refs))
	for i, r := range refs {
		s[i] = r.String()
	}
	return strings.Join(s, ", ")
}

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '-'
	}, s)
}

// settle polls the head revision until it has not moved for `quiet`, or
// `timeout` elapses. timedOut is reported to the caller, which records it
// as a gate failure: a controller that never goes quiet is a finding.
func settle(ctx context.Context, cap *timeline.Capturer, keep func(*beads.Issue) bool, quiet, timeout time.Duration, logf func(string, ...any)) (head int64, timedOut bool, err error) {
	deadline := time.Now().Add(timeout)
	head, err = cap.HeadWhereIssue(ctx, keep)
	if err != nil {
		return 0, false, err
	}
	lastMove := time.Now()
	logf("settling: scoped head=%d quiet=%s timeout=%s", head, quiet, timeout)
	for {
		if time.Since(lastMove) >= quiet {
			return head, false, nil
		}
		if time.Now().After(deadline) {
			logf("settle TIMEOUT at head=%d; the timeline did not go quiet for %s", head, quiet)
			return head, true, nil
		}
		select {
		case <-ctx.Done():
			return head, false, ctx.Err()
		case <-time.After(time.Second):
		}
		h, err := cap.HeadWhereIssue(ctx, keep)
		if err != nil {
			return 0, false, err
		}
		if h != head {
			head = h
			lastMove = time.Now()
		}
	}
}

func printVerdict(w io.Writer, v *gate.Verdict) {
	status := "PASS"
	if !v.Pass {
		status = "FAIL"
	}
	fmt.Fprintf(w, "%s  plan=%s range=%s source=%s events=%d  (%d passed, %d failed, %d skipped)\n",
		status, v.Plan, v.Range, v.Source, v.Events, v.Passed, v.Failed, v.Skipped)
	fmt.Fprintf(w, "      %s\n", v.Summary())
	for _, n := range v.Notes {
		fmt.Fprintf(w, "note: %s\n", n)
	}
	for _, ch := range v.Checks {
		mark := map[string]string{"pass": "ok  ", "fail": "FAIL", "skip": "skip"}[ch.Status]
		fmt.Fprintf(w, "  %s %-10s L%-3d %s\n", mark, ch.Section, ch.LineNo, ch.Text)
		if ch.Reason != "" {
			fmt.Fprintf(w, "       %s\n", ch.Reason)
		}
		for i, e := range ch.Evidence {
			if i == 8 {
				fmt.Fprintf(w, "       ... %d more\n", len(ch.Evidence)-8)
				break
			}
			fmt.Fprintf(w, "       - %s rev=%d %s %s\n", e.EventID, e.Rev, e.Op, kine.Short(e.Key))
		}
	}
}
