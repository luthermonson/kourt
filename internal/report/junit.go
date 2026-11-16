// Package report renders a gate.Suite for CI consumers.
package report

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/luthermonson/litecone/internal/gate"
)

// JUnit XML, in the shape GitHub Actions / Jenkins / GitLab publishers read:
// one <testsuite> per litecone invocation, one <testcase> per plan. A failed
// plan carries one <failure> whose text lists every failed check with its
// cited evidence (event ids + revisions); a plan that could not run is an
// <error>.
type junitSuites struct {
	XMLName  xml.Name     `xml:"testsuites"`
	Tests    int          `xml:"tests,attr"`
	Failures int          `xml:"failures,attr"`
	Errors   int          `xml:"errors,attr"`
	Time     string       `xml:"time,attr"`
	Suites   []junitSuite `xml:"testsuite"`
}

type junitSuite struct {
	Name      string      `xml:"name,attr"`
	Tests     int         `xml:"tests,attr"`
	Failures  int         `xml:"failures,attr"`
	Errors    int         `xml:"errors,attr"`
	Skipped   int         `xml:"skipped,attr"`
	Time      string      `xml:"time,attr"`
	Timestamp string      `xml:"timestamp,attr"`
	Cases     []junitCase `xml:"testcase"`
}

type junitCase struct {
	Name      string        `xml:"name,attr"`
	ClassName string        `xml:"classname,attr"`
	Time      string        `xml:"time,attr"`
	Failure   *junitMessage `xml:"failure,omitempty"`
	Error     *junitMessage `xml:"error,omitempty"`
	SystemOut string        `xml:"system-out,omitempty"`
}

type junitMessage struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr,omitempty"`
	Body    string `xml:",chardata"`
}

// WriteJUnit renders the suite as JUnit XML.
func WriteJUnit(w io.Writer, s *gate.Suite) error {
	js := junitSuite{Name: "litecone", Timestamp: s.StartedAt.UTC().Format("2006-01-02T15:04:05"), Time: fmt.Sprintf("%.3f", s.Duration)}
	for _, v := range s.Verdicts {
		tc := junitCase{Name: v.Plan, ClassName: "litecone.plan", Time: "0"}
		var out strings.Builder
		fmt.Fprintf(&out, "range=%s source=%s lossy=%v events=%d passed=%d failed=%d skipped=%d\n", v.Range, v.Source, v.Lossy, v.Events, v.Passed, v.Failed, v.Skipped)
		for _, n := range v.Notes {
			fmt.Fprintf(&out, "note: %s\n", n)
		}
		var fail strings.Builder
		for _, c := range v.Checks {
			fmt.Fprintf(&out, "[%s] %s L%d: %s\n", c.Status, c.Section, c.LineNo, c.Text)
			if c.Status != "fail" {
				continue
			}
			fmt.Fprintf(&fail, "%s L%d: %s\n  %s\n", c.Section, c.LineNo, c.Text, c.Reason)
			for _, e := range c.Evidence {
				fmt.Fprintf(&fail, "  evidence: %s rev=%d %s %s\n", e.EventID, e.Rev, e.Op, e.Key)
			}
		}
		tc.SystemOut = out.String()
		if !v.Pass {
			tc.Failure = &junitMessage{Message: fmt.Sprintf("%d gate check(s) failed", v.Failed), Type: "gate", Body: fail.String()}
			js.Failures++
		}
		js.Tests++
		js.Cases = append(js.Cases, tc)
	}
	for _, e := range s.Errors {
		js.Tests++
		js.Errors++
		js.Cases = append(js.Cases, junitCase{Name: e.Plan, ClassName: "litecone.plan", Time: "0",
			Error: &junitMessage{Message: "could not run", Type: "error", Body: e.Err}})
	}
	doc := junitSuites{Tests: js.Tests, Failures: js.Failures, Errors: js.Errors, Time: js.Time, Suites: []junitSuite{js}}
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}

// WriteJUnitFile writes the report to path.
func WriteJUnitFile(path string, s *gate.Suite) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return WriteJUnit(f, s)
}
