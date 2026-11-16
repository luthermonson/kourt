// Package packcheck validates the Gas City pack under ../../pack the way the
// contributing-to-gascity pack's own tests do: TOML parses, formulas have
// consistent step graphs and vars, skills' frontmatter names match their
// directories, agents have prompts, doctor scripts are executable, and the
// pack stays self-contained (no [imports.*]).
package packcheck

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

const root = "../../pack"

func TestPackIsSelfContained(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(root, "pack.toml"))
	if err != nil {
		t.Fatal(err)
	}
	var pack struct {
		Pack struct {
			Name    string `toml:"name"`
			Version string `toml:"version"`
			Schema  int    `toml:"schema"`
		} `toml:"pack"`
	}
	if err := toml.Unmarshal(b, &pack); err != nil {
		t.Fatal(err)
	}
	if pack.Pack.Name != "litecone" || pack.Pack.Schema != 2 || pack.Pack.Version == "" {
		t.Fatalf("pack = %+v", pack.Pack)
	}
	if regexp.MustCompile(`(?m)^\[imports\.`).Match(b) {
		t.Fatal("litecone pack must not import any pack — it is self-contained")
	}
}

type formula struct {
	Formula     string `toml:"formula"`
	Description string `toml:"description"`
	Vars        map[string]struct {
		Description string `toml:"description"`
		Required    bool   `toml:"required"`
		Default     *string `toml:"default"`
	} `toml:"vars"`
	Steps []struct {
		ID          string   `toml:"id"`
		Title       string   `toml:"title"`
		Description string   `toml:"description"`
		Needs       []string `toml:"needs"`
		Assignee    string   `toml:"assignee"`
	} `toml:"steps"`
}

func TestFormulas(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(root, "formulas", "*.formula.toml"))
	want := map[string]bool{"mol-litecone-run": true, "mol-litecone-judge": true, "mol-litecone-test": true}
	if len(files) != len(want) {
		t.Fatalf("formulas = %v", files)
	}
	varRef := regexp.MustCompile(`\{\{([a-z_]+)\}\}`)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var fm formula
		if err := toml.Unmarshal(b, &fm); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		base := strings.TrimSuffix(filepath.Base(f), ".formula.toml")
		if fm.Formula != base || !want[base] {
			t.Errorf("%s: formula name %q must match the file name", f, fm.Formula)
		}
		if !strings.Contains(fm.Description, "gc sling --formula "+fm.Formula) {
			t.Errorf("%s: description must show the sling invocation", f)
		}
		if _, ok := fm.Vars["plan"]; !ok {
			t.Errorf("%s: every formula takes --var plan=", f)
		}
		for name, v := range fm.Vars {
			if v.Required && v.Default != nil {
				t.Errorf("%s: vars.%s cannot have both required=true and a default", f, name)
			}
			if !v.Required && v.Default == nil {
				t.Errorf("%s: vars.%s needs a default or required=true", f, name)
			}
		}
		ids := map[string]bool{}
		for _, s := range fm.Steps {
			if s.ID == "" || s.Title == "" || s.Description == "" {
				t.Errorf("%s: step %+v needs id, title, description", f, s.ID)
			}
			if ids[s.ID] {
				t.Errorf("%s: duplicate step id %s", f, s.ID)
			}
			ids[s.ID] = true
		}
		for _, s := range fm.Steps {
			for _, n := range s.Needs {
				if !ids[n] {
					t.Errorf("%s: step %s needs unknown step %s", f, s.ID, n)
				}
			}
			for _, m := range varRef.FindAllStringSubmatch(s.Description+s.Title, -1) {
				if _, ok := fm.Vars[m[1]]; !ok {
					t.Errorf("%s: step %s references undeclared var {{%s}}", f, s.ID, m[1])
				}
			}
			if s.ID == "judge" && s.Assignee != "litecone-judge" {
				t.Errorf("%s: the judge step must be assigned to the litecone-judge agent", f)
			}
			if s.ID == "judge" && !strings.Contains(s.Description, "skills/litecone-judge/SKILL.md") {
				t.Errorf("%s: the judge step must delegate to the litecone-judge skill", f)
			}
		}
		if fm.Steps[0].Needs != nil {
			t.Errorf("%s: first step must have no needs", f)
		}
	}
}

func TestSkills(t *testing.T) {
	dirs, _ := filepath.Glob(filepath.Join(root, "skills", "*", "SKILL.md"))
	got := map[string]bool{}
	for _, s := range dirs {
		b, err := os.ReadFile(s)
		if err != nil {
			t.Fatal(err)
		}
		m := regexp.MustCompile(`(?m)^name:\s*(\S+)`).FindSubmatch(b)
		if m == nil {
			t.Fatalf("%s: missing frontmatter name", s)
		}
		dir := filepath.Base(filepath.Dir(s))
		if string(m[1]) != dir {
			t.Errorf("%s: frontmatter name %s must match directory %s", s, m[1], dir)
		}
		if !regexp.MustCompile(`(?m)^description:\s*\S`).Match(b) {
			t.Errorf("%s: missing frontmatter description", s)
		}
		got[dir] = true
	}
	for _, want := range []string{"litecone-author", "litecone-judge"} {
		if !got[want] {
			t.Errorf("missing skill %s", want)
		}
	}
}

func TestAgents(t *testing.T) {
	agent := filepath.Join(root, "agents", "litecone-judge")
	b, err := os.ReadFile(filepath.Join(agent, "agent.toml"))
	if err != nil {
		t.Fatal(err)
	}
	var a map[string]any
	if err := toml.Unmarshal(b, &a); err != nil {
		t.Fatal(err)
	}
	if _, ok := a["description"]; !ok {
		t.Error("agent.toml needs a description")
	}
	if _, ok := a["fallback"]; ok {
		t.Error("`fallback` was removed from v2 agent.toml")
	}
	if _, err := os.Stat(filepath.Join(agent, "prompt.template.md")); err != nil {
		t.Error("agent needs prompt.template.md")
	}
}

func TestOrders(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(root, "orders", "*.toml"))
	if len(files) == 0 {
		t.Fatal("expected orders")
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		var o struct {
			Order struct {
				Description string `toml:"description"`
				Formula     string `toml:"formula"`
				Exec        string `toml:"exec"`
				Trigger     string `toml:"trigger"`
				Schedule    string `toml:"schedule"`
				Interval    string `toml:"interval"`
				Enabled     *bool  `toml:"enabled"`
			} `toml:"order"`
		}
		if err := toml.Unmarshal(b, &o); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if (o.Order.Formula == "") == (o.Order.Exec == "") {
			t.Errorf("%s: an order names a formula OR an exec, never both/neither", f)
		}
		switch o.Order.Trigger {
		case "cron":
			if o.Order.Schedule == "" {
				t.Errorf("%s: cron needs schedule", f)
			}
		case "cooldown":
			if o.Order.Interval == "" {
				t.Errorf("%s: cooldown needs interval", f)
			}
		default:
			t.Errorf("%s: unexpected trigger %q", f, o.Order.Trigger)
		}
		if o.Order.Enabled == nil || *o.Order.Enabled {
			t.Errorf("%s: orders ship disabled", f)
		}
	}
}

func TestDoctor(t *testing.T) {
	scripts, _ := filepath.Glob(filepath.Join(root, "doctor", "check-*.sh"))
	if len(scripts) != 2 {
		t.Fatalf("doctor scripts = %v", scripts)
	}
	for _, s := range scripts {
		st, err := os.Stat(s)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode()&0o111 == 0 {
			t.Errorf("%s must be executable (release hashes depend on the +x bit)", s)
		}
	}
	for _, d := range []string{"cluster", "beads"} {
		b, err := os.ReadFile(filepath.Join(root, "doctor", d, "doctor.toml"))
		if err != nil {
			t.Fatal(err)
		}
		var dt struct {
			Description string `toml:"description"`
			Run         string `toml:"run"`
		}
		if err := toml.Unmarshal(b, &dt); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(root, "doctor", d, dt.Run)); err != nil {
			t.Errorf("doctor/%s: run script %s not found", d, dt.Run)
		}
	}
}
