package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// These tests pin the CI contract: 0 = pass, 1 = gate failure, 2 = could
// not run. They drive the real CLI entry point (run) offline through
// `replay --run` against the fixture timeline in testdata/.

func runCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, err bytes.Buffer
	code = run(context.Background(), args, &out, &err)
	return code, out.String(), err.String()
}

func TestExitPass(t *testing.T) {
	code, out, _ := runCLI(t, "replay", "testdata/must-pass.plan.md", "--run", "testdata/scale.run.json")
	if code != 0 {
		t.Fatalf("exit %d, want 0\n%s", code, out)
	}
	if !strings.HasPrefix(out, "PASS") || !strings.Contains(out, "exit=0") {
		t.Fatalf("unexpected output:\n%s", out)
	}
}

func TestExitFailOnTrippedNeverAndUnmetExpect(t *testing.T) {
	code, out, _ := runCLI(t, "replay", "testdata/must-fail.plan.md", "--run", "testdata/scale.run.json")
	if code != 1 {
		t.Fatalf("exit %d, want 1 (gate failure)\n%s", code, out)
	}
	if !strings.HasPrefix(out, "FAIL") {
		t.Fatalf("expected FAIL verdict:\n%s", out)
	}
	// Both failure modes must be visible and cited.
	if !strings.Contains(out, "needed exactly 1 created event(s) for replicasets/podinfo-*") {
		t.Errorf("unmet EXPECT not reported:\n%s", out)
	}
	if !strings.Contains(out, "outside the ownership subtree") || !strings.Contains(out, "configmaps/kube-system/stray") {
		t.Errorf("tripped NEVER not reported with evidence:\n%s", out)
	}
	if !strings.Contains(out, "seq:6 rev=106") {
		t.Errorf("evidence should cite the offending event id + revision:\n%s", out)
	}
}

func TestExitErrorOnNoAssertions(t *testing.T) {
	// Fail-closed: a plan with nothing machine-checkable cannot pass.
	code, _, _ := runCLI(t, "lint", "testdata/no-asserts.plan.md")
	if code != 2 {
		t.Fatalf("lint exit %d, want 2", code)
	}
	code, out, _ := runCLI(t, "replay", "testdata/no-asserts.plan.md", "--run", "testdata/scale.run.json")
	if code != 1 || !strings.Contains(out, "fail-closed") {
		t.Fatalf("replay exit %d, want 1 with the fail-closed guard:\n%s", code, out)
	}
}

func TestExitErrorOnBadSyntax(t *testing.T) {
	code, _, _ := runCLI(t, "lint", "testdata/bad-syntax.plan.md")
	if code != 2 {
		t.Fatalf("lint exit %d, want 2", code)
	}
	code, _, stderr := runCLI(t, "replay", "testdata/bad-syntax.plan.md", "--run", "testdata/scale.run.json")
	if code != 2 || !strings.Contains(stderr, "bad-syntax.plan.md L") {
		t.Fatalf("replay exit %d, want 2:\n%s", code, stderr)
	}
}

func TestExitErrorOnMissingInputs(t *testing.T) {
	if code, _, _ := runCLI(t, "replay", "testdata/nope.plan.md", "--run", "testdata/scale.run.json"); code != 2 {
		t.Fatalf("missing plan: exit %d, want 2", code)
	}
	if code, _, _ := runCLI(t, "replay", "testdata/must-pass.plan.md", "--run", "testdata/nope.json"); code != 2 {
		t.Fatalf("missing run: exit %d, want 2", code)
	}
	if code, _, _ := runCLI(t, "replay", "testdata/must-pass.plan.md"); code != 2 {
		t.Fatalf("no source: exit %d, want 2", code)
	}
	if code, _, _ := runCLI(t, "bogus-command"); code != 2 {
		t.Fatalf("unknown command: exit %d, want 2", code)
	}
	if code, _, _ := runCLI(t, "diff", "--beads", "http://127.0.0.1:1", "1", "2"); code != 2 {
		t.Fatalf("unreachable server: exit %d, want 2", code)
	}
}

func TestLintDirectoryAggregates(t *testing.T) {
	// A directory with one broken plan is an error for the whole call.
	code, out, _ := runCLI(t, "lint", "testdata")
	if code != 2 {
		t.Fatalf("exit %d, want 2\n%s", code, out)
	}
	if !strings.Contains(out, "fixture-must-pass") || !strings.Contains(out, "fixture-bad-syntax") {
		t.Fatalf("directory expansion missed plans:\n%s", out)
	}
}

type junitDoc struct {
	Tests    int `xml:"tests,attr"`
	Failures int `xml:"failures,attr"`
	Errors   int `xml:"errors,attr"`
	Suites   []struct {
		Cases []struct {
			Name    string `xml:"name,attr"`
			Failure *struct {
				Message string `xml:"message,attr"`
				Body    string `xml:",chardata"`
			} `xml:"failure"`
		} `xml:"testcase"`
	} `xml:"testsuite"`
}

func TestJUnitMarksFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "junit.xml")
	code, _, _ := runCLI(t, "replay", "testdata/must-fail.plan.md", "--run", "testdata/scale.run.json", "--report", "junit", "--report-path", path)
	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc junitDoc
	if err := xml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("invalid junit xml: %v\n%s", err, b)
	}
	// One testcase per plan plus one skipped testcase per SKIPPED assertion
	// (the `[rs] then` edge is not evaluable when its side matched nothing).
	if doc.Failures != 1 || doc.Errors != 0 || doc.Tests < 1 {
		t.Fatalf("tests=%d failures=%d errors=%d", doc.Tests, doc.Failures, doc.Errors)
	}
	tc := doc.Suites[0].Cases[0]
	if tc.Name != "fixture-must-fail" || tc.Failure == nil {
		t.Fatalf("testcase not marked failed: %+v", tc)
	}
	if !strings.Contains(tc.Failure.Body, "evidence: seq:6 rev=106") {
		t.Fatalf("failure must carry cited evidence:\n%s", tc.Failure.Body)
	}

	// A passing plan produces zero failures, and its SKIPPED field assertion
	// shows up as a skipped testcase rather than vanishing.
	path2 := filepath.Join(t.TempDir(), "junit.xml")
	code, out, _ := runCLI(t, "replay", "testdata/must-pass.plan.md", "--run", "testdata/scale.run.json", "--report", "junit", "--report-path", path2)
	if code != 0 {
		t.Fatalf("exit %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "PASSED structural checks; 1 assertion(s) SKIPPED") {
		t.Fatalf("skipped assertions must be prominent on stdout:\n%s", out)
	}
	b, _ = os.ReadFile(path2)
	var ok junitDoc
	_ = xml.Unmarshal(b, &ok)
	if ok.Failures != 0 || ok.Suites[0].Cases[0].Failure != nil {
		t.Fatalf("passing plan marked failed:\n%s", b)
	}
	if ok.Tests != 2 || !strings.Contains(string(b), "<skipped message=\"field-decode pending") {
		t.Fatalf("expected one plan case + one skipped assertion case:\n%s", b)
	}
}

// TestBinaryExitCode builds the real binary and runs it, proving the
// process exit status (what a CI step sees) matches the contract.
func TestBinaryExitCode(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
	bin := filepath.Join(t.TempDir(), "kourt")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, ".")
	if outb, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, outb)
	}
	cases := []struct {
		args []string
		want int
	}{
		{[]string{"replay", "testdata/must-pass.plan.md", "--run", "testdata/scale.run.json"}, 0},
		{[]string{"replay", "testdata/must-fail.plan.md", "--run", "testdata/scale.run.json"}, 1},
		{[]string{"lint", "testdata/bad-syntax.plan.md"}, 2},
		{[]string{"replay", "testdata/no-asserts.plan.md", "--run", "testdata/scale.run.json"}, 1},
	}
	for _, c := range cases {
		cmd := exec.Command(bin, c.args...)
		outb, _ := cmd.CombinedOutput()
		got := cmd.ProcessState.ExitCode()
		if got != c.want {
			t.Errorf("%v: exit %d, want %d\n%s", c.args, got, c.want, outb)
		}
	}
}
