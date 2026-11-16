package gate

import (
	"strconv"
	"strings"
	"testing"

	"github.com/luthermonson/kourt/internal/plan"
	"github.com/luthermonson/kourt/internal/timeline"
)

// ---- synthetic timelines -------------------------------------------------------

type tl struct {
	events []timeline.Event
	owners map[string][]string
	seq    int64
}

func newTL() *tl { return &tl{owners: map[string][]string{}} }

// ev appends an event at rev for key with the given owner chain.
func (t *tl) ev(rev int64, op timeline.Op, key string, owners ...string) {
	t.seq++
	pk := strings.Split(strings.TrimPrefix(key, "/registry/"), "/")
	t.events = append(t.events, timeline.Event{
		ID: "seq:" + itoa(t.seq), Seq: t.seq, Rev: rev, Op: op, Key: key, Kind: pk[0], Name: pk[len(pk)-1], BeadID: "b" + itoa(t.seq),
	})
	if _, ok := t.owners[key]; !ok || len(owners) > 0 {
		t.owners[key] = owners
	}
}

func (t *tl) run(from, to int64) *timeline.Run {
	return &timeline.Run{Source: "journal", Range: timeline.Range{From: from, To: to}, Events: t.events, Owners: t.owners}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

const (
	kCluster = "/registry/cluster.x-k8s.io/clusters/default/demo"
	kMC      = "/registry/infrastructure.cluster.x-k8s.io/mirageclusters/default/demo"
	kKCP     = "/registry/controlplane.cluster.x-k8s.io/kubeadmcontrolplanes/default/demo-cp"
	kMD      = "/registry/cluster.x-k8s.io/machinedeployments/default/demo-md"
	kMS      = "/registry/cluster.x-k8s.io/machinesets/default/demo-md-7f9c"
	kMcp     = "/registry/cluster.x-k8s.io/machines/default/demo-cp-x1"
	kMw1     = "/registry/cluster.x-k8s.io/machines/default/demo-md-7f9c-a"
	kMw2     = "/registry/cluster.x-k8s.io/machines/default/demo-md-7f9c-b"
	kMMcp    = "/registry/infrastructure.cluster.x-k8s.io/miragemachines/default/demo-cp-x1"
	kMMw1    = "/registry/infrastructure.cluster.x-k8s.io/miragemachines/default/demo-md-7f9c-a"
	kMMw2    = "/registry/infrastructure.cluster.x-k8s.io/miragemachines/default/demo-md-7f9c-b"
	kNcp     = "/registry/minions/demo-cp-x1"
	kNw1     = "/registry/minions/demo-md-7f9c-a"
	kNw2     = "/registry/minions/demo-md-7f9c-b"
	kStray   = "/registry/configmaps/kube-system/stray"
)

// clusterCreate is a timeline that satisfies plans/examples/capi-cluster-create.
func clusterCreate() *tl {
	t := newTL()
	// [apply]
	t.ev(1001, timeline.OpCreate, kCluster)
	t.ev(1002, timeline.OpCreate, kMC, kCluster)
	t.ev(1003, timeline.OpCreate, kKCP, kCluster)
	t.ev(1004, timeline.OpCreate, kMD, kCluster)
	// infra reconciles (field-level, invisible to the gate but real writes)
	t.ev(1010, timeline.OpUpdate, kMC, kCluster)
	t.ev(1011, timeline.OpUpdate, kCluster)
	// [workers] object creation interleaves with [controlplane]
	t.ev(1020, timeline.OpCreate, kMS, kMD, kCluster)
	t.ev(1021, timeline.OpCreate, kMcp, kKCP, kCluster)
	t.ev(1022, timeline.OpCreate, kMw1, kMS, kMD, kCluster)
	t.ev(1023, timeline.OpCreate, kMw2, kMS, kMD, kCluster)
	t.ev(1024, timeline.OpCreate, kMMcp, kMcp, kKCP, kCluster)
	t.ev(1025, timeline.OpCreate, kMMw1, kMw1, kMS, kMD, kCluster)
	t.ev(1026, timeline.OpCreate, kMMw2, kMw2, kMS, kMD, kCluster)
	t.ev(1030, timeline.OpUpdate, kMcp, kKCP, kCluster)
	t.ev(1031, timeline.OpUpdate, kKCP, kCluster)
	// [nodes] after controlplane
	t.ev(1040, timeline.OpCreate, kNcp)
	t.ev(1041, timeline.OpCreate, kNw1)
	t.ev(1042, timeline.OpCreate, kNw2)
	// [ready]
	t.ev(1050, timeline.OpUpdate, kCluster)
	t.ev(1051, timeline.OpUpdate, kMD, kCluster)
	return t
}

func parseFile(t *testing.T, path string) *plan.Plan {
	t.Helper()
	p, err := plan.ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, errs := p.Counts(); errs != 0 {
		for _, l := range p.Lines {
			if l.Err != "" {
				t.Errorf("L%d %q: %s", l.LineNo, l.Text, l.Err)
			}
		}
		t.Fatalf("%s has %d parse errors", path, errs)
	}
	return p
}

func parse(t *testing.T, src string) *plan.Plan {
	t.Helper()
	p, err := plan.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func find(v *Verdict, text string) *Check {
	for i := range v.Checks {
		if strings.Contains(v.Checks[i].Text, text) {
			return &v.Checks[i]
		}
	}
	return nil
}

func dump(t *testing.T, v *Verdict) {
	t.Helper()
	for _, c := range v.Checks {
		t.Logf("[%s] %s L%d %s -- %s", c.Status, c.Section, c.LineNo, c.Text, c.Reason)
	}
}

// ---- the two example plans -------------------------------------------------------

func TestCAPIClusterCreatePasses(t *testing.T) {
	p := parseFile(t, "../../plans/examples/capi-cluster-create/capi-cluster-create.plan.md")
	v := Judge(p, clusterCreate().run(1000, 1100))
	if !v.Pass {
		dump(t, v)
		t.Fatal("structural checks should pass")
	}
	if v.Skipped == 0 {
		t.Fatal("field/provenance assertions must be reported SKIPPED, not absent")
	}
	if !strings.Contains(v.Summary(), "SKIPPED") || !strings.HasPrefix(v.Summary(), "PASSED structural") {
		t.Fatalf("summary = %q", v.Summary())
	}
	// Every skipped check names why.
	for _, c := range v.Checks {
		if c.Status == "skip" && !strings.Contains(c.Reason, "pending") && !strings.Contains(c.Reason, "not evaluable") && !strings.Contains(c.Reason, "prose") {
			t.Errorf("skip without a roadmap reason: %+v", c)
		}
	}
	// created-by resolved through the owner chain.
	if c := find(v, "machines/demo-md-* ×2 created-by"); c == nil || c.Status != "pass" || len(c.Evidence) != 2 {
		t.Fatalf("created-by: %+v", c)
	}
	// Ordering edges evaluated with evidence.
	if c := find(v, "[nodes] after controlplane"); c == nil || c.Status != "pass" || len(c.Evidence) != 2 {
		t.Fatalf("after edge: %+v", c)
	}
	if c := find(v, "[workers] concurrent with controlplane"); c == nil || c.Status != "pass" {
		t.Fatalf("concurrent edge: %+v", c)
	}
	// `then` onto a group of only field matchers is SKIPPED, not passed.
	if c := find(v, "[infra] then"); c == nil || c.Status != "skip" {
		t.Fatalf("then onto field-only group: %+v", c)
	}
	if c := find(v, "[ready] finally"); c == nil || c.Status != "skip" {
		t.Fatalf("finally on field-only group: %+v", c)
	}
	// Provenance NEVER line is skipped.
	if c := find(v, "ONLY actor"); c == nil || c.Status != "skip" || !strings.Contains(c.Reason, "provenance") {
		t.Fatalf("provenance: %+v", c)
	}
}

func TestCAPIClusterCreateFailsOnOrderAndElsewhere(t *testing.T) {
	p := parseFile(t, "../../plans/examples/capi-cluster-create/capi-cluster-create.plan.md")
	tl := clusterCreate()
	// A node registers before the control plane machine exists, and a stray
	// configmap in kube-system is written.
	tl.ev(1005, timeline.OpCreate, "/registry/minions/demo-md-early")
	tl.ev(1006, timeline.OpUpdate, kStray)
	v := Judge(p, tl.run(1000, 1100))
	if v.Pass {
		dump(t, v)
		t.Fatal("expected failure")
	}
	if c := find(v, "nodes/demo-md-* ×2 created"); c == nil || c.Status != "fail" || !strings.Contains(c.Reason, "found 3") {
		t.Fatalf("exact count must fail on 3: %+v", c)
	}
	if c := find(v, "[nodes] after controlplane"); c == nil || c.Status != "fail" {
		t.Fatalf("after edge must fail: %+v", c)
	}
	if c := find(v, "elsewhere"); c == nil || c.Status != "fail" || len(c.Evidence) != 1 || c.Evidence[0].Key != kStray {
		t.Fatalf("elsewhere must cite the stray configmap only: %+v", c)
	}
}

func TestCAPIClusterCreateMachineDeleteTripsNever(t *testing.T) {
	p := parseFile(t, "../../plans/examples/capi-cluster-create/capi-cluster-create.plan.md")
	tl := clusterCreate()
	tl.ev(1060, timeline.OpDelete, kMcp, kKCP, kCluster)
	v := Judge(p, tl.run(1000, 1100))
	if c := find(v, "machines/demo-cp-* deleted"); c == nil || c.Status != "fail" || c.Evidence[0].Rev != 1060 {
		t.Fatalf("NEVER deleted must trip with evidence: %+v", c)
	}
	if c := find(v, "machines/demo-* ×0 deleted"); c == nil || c.Status != "fail" {
		t.Fatalf("THROUGHOUT ×0 deleted must fail: %+v", c)
	}
}

const (
	kPod1 = "/registry/pods/default/scale-test-1"
	kPod2 = "/registry/pods/default/scale-test-2"
	kPod3 = "/registry/pods/default/scale-test-3"
	kPod4 = "/registry/pods/default/scale-test-4"
	kMw3  = "/registry/cluster.x-k8s.io/machines/default/demo-md-7f9c-c"
	kMMw3 = "/registry/infrastructure.cluster.x-k8s.io/miragemachines/default/demo-md-7f9c-c"
	kNw3  = "/registry/minions/demo-md-7f9c-c"
)

func autoscaleUp() *tl {
	t := newTL()
	for i, k := range []string{kPod1, kPod2, kPod3, kPod4} {
		t.ev(2001+int64(i), timeline.OpCreate, k)
		t.ev(2005+int64(i), timeline.OpUpdate, k) // scheduler: PodScheduled=False
	}
	t.ev(2010, timeline.OpUpdate, kMD, kCluster)       // autoscaler bumps replicas
	t.ev(2011, timeline.OpUpdate, kMS, kMD, kCluster)   // MD controller propagates
	t.ev(2012, timeline.OpCreate, kMw2, kMS, kMD, kCluster)
	t.ev(2013, timeline.OpCreate, kMw3, kMS, kMD, kCluster)
	t.ev(2014, timeline.OpCreate, kMMw2, kMw2, kMS, kMD, kCluster)
	t.ev(2015, timeline.OpCreate, kMMw3, kMw3, kMS, kMD, kCluster)
	t.ev(2020, timeline.OpCreate, kNw2)
	t.ev(2021, timeline.OpCreate, kNw3)
	for i, k := range []string{kPod1, kPod2, kPod3, kPod4} {
		t.ev(2030+int64(i), timeline.OpUpdate, k) // binding
	}
	return t
}

func TestCAPIAutoscaleUpPasses(t *testing.T) {
	p := parseFile(t, "../../plans/examples/capi-autoscale-up/capi-autoscale-up.plan.md")
	v := Judge(p, autoscaleUp().run(2000, 2100))
	if !v.Pass {
		dump(t, v)
		t.Fatal("structural checks should pass")
	}
	if c := find(v, "[scale] then"); c == nil || c.Status != "pass" {
		t.Fatalf("then edge: %+v", c)
	}
	if c := find(v, "[nodes] after scale"); c == nil || c.Status != "pass" {
		t.Fatalf("after edge: %+v", c)
	}
	if c := find(v, "[bind] after nodes"); c == nil || c.Status != "skip" {
		t.Fatalf("field-only group edge must skip: %+v", c)
	}
	if c := find(v, "×1+ updated"); c == nil || c.Status != "pass" {
		t.Fatalf("at-least count: %+v", c)
	}
}

func TestCAPIAutoscaleUpRolloutInsteadOfScaleFails(t *testing.T) {
	p := parseFile(t, "../../plans/examples/capi-autoscale-up/capi-autoscale-up.plan.md")
	tl := autoscaleUp()
	// A new MachineSet appears and the control plane machine is touched.
	tl.ev(2016, timeline.OpCreate, "/registry/cluster.x-k8s.io/machinesets/default/demo-md-new", kMD, kCluster)
	tl.ev(2017, timeline.OpUpdate, kMcp, kKCP, kCluster)
	v := Judge(p, tl.run(2000, 2100))
	if v.Pass {
		dump(t, v)
		t.Fatal("expected failure")
	}
	if c := find(v, "machinesets/demo-md-* ×0 created"); c == nil || c.Status != "fail" {
		t.Fatalf("rollout must trip the ×0 invariant: %+v", c)
	}
	if c := find(v, "machines/demo-cp-* changed"); c == nil || c.Status != "fail" {
		t.Fatalf("control plane touch must trip NEVER: %+v", c)
	}
	// The scale group also fails: the machines must come before the nodes (still true) but
	// the cp machine update is outside the MachineDeployment subtree and not excepted.
	if c := find(v, "elsewhere"); c == nil || c.Status != "fail" {
		t.Fatalf("elsewhere must trip on the control-plane machine: %+v", c)
	}
}

// ---- unit behaviour --------------------------------------------------------------

func TestAbsenceIsNotPass(t *testing.T) {
	// An EXPECT line whose event never arrives fails.
	p := parse(t, "# PLAN x\n## SUBTREE\n- clusters/demo\n## EXPECT\n- machines/demo-cp-* ×1 created\n")
	v := Judge(p, newTL().run(0, 10))
	if v.Pass || !strings.Contains(v.Checks[0].Reason, "found 0") {
		t.Fatalf("missing event must fail: %+v", v.Checks)
	}
}

func TestExplicitNothingHappened(t *testing.T) {
	p := parse(t, "# PLAN x\n## THROUGHOUT\n- */* ×0 changed\n")
	if v := Judge(p, newTL().run(0, 10)); !v.Pass {
		t.Fatalf("explicit ×0 on an empty window passes: %+v", v.Checks)
	}
	if v := Judge(p, clusterCreate().run(1000, 1100)); v.Pass {
		t.Fatal("explicit ×0 fails when something happened")
	}
}

func TestMatchRef(t *testing.T) {
	cases := []struct {
		ref  string
		key  string
		kind string
		want bool
	}{
		{"machines/demo-md-*", kMw1, "cluster.x-k8s.io", true},  // grouped key: resource segment wins over issue_type
		{"cluster.x-k8s.io/demo-*", kMw1, "cluster.x-k8s.io", true}, // issue_type also matches
		{"nodes/demo-*", kNw1, "minions", true},                   // legacy minions alias
		{"pods/scale-test-*", kPod1, "pods", true},
		{"pods/scale-test-*", "/registry/pods/other/scale-test-9", "pods", true}, // namespace not part of the ref
		{"pods/default/scale-test-*", "/registry/pods/other/scale-test-9", "pods", false},
		{"services/web", "/registry/services/specs/default/web", "services", true},
		{"machines/demo-cp-*", kMw1, "cluster.x-k8s.io", false},
	}
	for _, c := range cases {
		ref, _ := plan.ParseRef(c.ref)
		pk := strings.Split(strings.TrimPrefix(c.key, "/registry/"), "/")
		e := timeline.Event{Key: c.key, Kind: c.kind, Name: pk[len(pk)-1]}
		if got := MatchRef(ref, e); got != c.want {
			t.Errorf("MatchRef(%s, %s) = %v", c.ref, c.key, got)
		}
	}
}

func TestInSubtree(t *testing.T) {
	roots := []plan.Ref{{Kind: "clusters", Name: "demo"}}
	e := timeline.Event{Key: kMw1, Kind: "cluster.x-k8s.io", Name: "demo-md-7f9c-a"}
	if !InSubtree(roots, e, []string{kMS, kMD, kCluster}) {
		t.Fatal("grandchild via owner chain")
	}
	if InSubtree(roots, e, []string{kMS, kMD}) {
		t.Fatal("chain not reaching the root")
	}
	if !InSubtreeKey(roots, kCluster, nil) {
		t.Fatal("root itself")
	}
}

func TestLossyNoted(t *testing.T) {
	p := parse(t, "# PLAN x\n## THROUGHOUT\n- */* ×0 changed\n")
	r := newTL().run(0, 10)
	r.Lossy = true
	v := Judge(p, r)
	if len(v.Notes) == 0 || !strings.Contains(v.Notes[0], "lossy") {
		t.Fatal("lossy run must be noted")
	}
}

func TestParseErrorFailsTheGate(t *testing.T) {
	p := parse(t, "# PLAN x\n## EXPECT\n- machines/x ×2 exploded\n")
	v := Judge(p, clusterCreate().run(1000, 1100))
	if v.Pass || !strings.Contains(v.Checks[0].Reason, "does not parse") {
		t.Fatalf("malformed directive must fail: %+v", v.Checks)
	}
}
