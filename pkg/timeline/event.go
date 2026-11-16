// Package timeline is kourt's event model: the ordered, revision-stamped
// record of what happened to k8s objects (beads) between two kine revisions,
// plus the ownership edges needed to reason about an event's light cone.
//
// Two sources can produce a timeline:
//
//   - journal:  the beads events journal (GET /v0/beads/events). Every
//     committed mutation, in commit order, with the full bead after each
//     write. Exact. Requires `events-journal true` on the workspace.
//   - snapshot: the current bead set (GET /v0/beads/issues). Each bead
//     carries its createRev (cr) and modRev (mr), so a window (from, to] can
//     be reconstructed from those two points per object only. LOSSY: writes
//     between cr and mr are invisible, and an object modified again after
//     `to` hides the write that fell inside the window.
//
// Causality is built on three things only: revision order, ownership edges
// (ownerReferences as beads parent-child dependencies) and counts. Not on the
// actor: the substrate stamps every write with actor "kine" today.
package timeline

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"
)

// Op is what happened to an object at a revision.
type Op string

const (
	OpCreate    Op = "create"
	OpUpdate    Op = "update"
	OpDelete    Op = "delete"
	OpDepAdd    Op = "dep_add"    // an ownership edge was recorded (child -> owner)
	OpDepRemove Op = "dep_remove" // an ownership edge was removed
)

// IsObjectOp reports whether the op mutates an object (as opposed to an edge).
func (o Op) IsObjectOp() bool {
	return o == OpCreate || o == OpUpdate || o == OpDelete
}

// Event is one point on the timeline.
type Event struct {
	// ID is the citable evidence id: "seq:<journal seq>" for journal events,
	// "snap:<bead>@<rev>" for snapshot-derived ones.
	ID string `json:"id"`
	// Seq is the journal sequence (0 for snapshot events).
	Seq int64 `json:"seq,omitempty"`
	// Rev is the kine global revision at which the write landed (mr, or cr
	// for a create).
	Rev int64 `json:"rev"`
	Op  Op    `json:"op"`
	// Key is the etcd key (/registry/<resource>/<ns>/<name>).
	Key       string `json:"key"`
	Kind      string `json:"kind"` // bead issue_type (pods, deployments, ...)
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
	BeadID    string `json:"bead_id"`
	Actor     string `json:"actor,omitempty"`
	At        string `json:"at,omitempty"` // RFC 3339 as the server stamped it
	CreateRev int64  `json:"create_rev,omitempty"`
	// Owner is the owner key for dep_add/dep_remove events.
	Owner string `json:"owner,omitempty"`
}

// Range is a half-open window of kine revisions: From < rev <= To. To == 0
// means "the head at capture time".
type Range struct {
	From int64 `json:"from"`
	To   int64 `json:"to"`
}

// Contains reports whether rev falls inside the window.
func (r Range) Contains(rev int64) bool {
	if rev <= r.From {
		return false
	}
	return r.To == 0 || rev <= r.To
}

func (r Range) String() string {
	if r.To == 0 {
		return fmt.Sprintf("%d..head", r.From)
	}
	return fmt.Sprintf("%d..%d", r.From, r.To)
}

// Run is a captured timeline: the thing `kourt record` writes and
// `kourt replay` reads. It is self-sufficient for offline judging.
type Run struct {
	Version    string    `json:"kourt"`
	Source     string    `json:"source"` // journal | snapshot
	Lossy      bool      `json:"lossy"`
	Server     string    `json:"server"`
	Range      Range     `json:"range"`
	Head       int64     `json:"head"` // highest kine revision observed at capture
	CapturedAt time.Time `json:"captured_at"`
	Plan       string    `json:"plan,omitempty"`
	Events     []Event   `json:"events"`
	// Owners maps an object key to its owner chain, nearest owner first,
	// as resolved from beads parent-child edges at capture time. It covers
	// every key touched by an event in the run.
	Owners map[string][]string `json:"owners"`
	Notes  []string            `json:"notes,omitempty"`
}

// Version is the run file format version.
const Version = "1"

// Sort orders events by (rev, seq, key) so journal and snapshot runs compare
// alike. Journal seq is the tie-breaker inside one revision.
func Sort(events []Event) {
	sort.SliceStable(events, func(i, j int) bool {
		a, b := events[i], events[j]
		if a.Rev != b.Rev {
			return a.Rev < b.Rev
		}
		if a.Seq != b.Seq {
			return a.Seq < b.Seq
		}
		return a.Key < b.Key
	})
}

// Load reads a run file.
func Load(path string) (*Run, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Run
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if r.Owners == nil {
		r.Owners = map[string][]string{}
	}
	Sort(r.Events)
	return &r, nil
}

// Save writes a run file.
func (r *Run) Save(path string) error {
	Sort(r.Events)
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// Ancestors returns the owner chain of key (nearest first), or nil.
func (r *Run) Ancestors(key string) []string {
	return r.Owners[key]
}
