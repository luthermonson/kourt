package timeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/luthermonson/kourt/pkg/beads"
	"github.com/luthermonson/kourt/pkg/kine"
)

// Source selects where a timeline comes from.
type Source string

const (
	SourceAuto     Source = "auto"     // journal when enabled, else snapshot
	SourceJournal  Source = "journal"  // require the journal
	SourceSnapshot Source = "snapshot" // force the lossy reconstruction
)

// ParseSource validates a --source flag value.
func ParseSource(s string) (Source, error) {
	switch Source(s) {
	case SourceAuto, SourceJournal, SourceSnapshot:
		return Source(s), nil
	case "":
		return SourceAuto, nil
	}
	return "", fmt.Errorf("unknown source %q (auto|journal|snapshot)", s)
}

// Label is the label the kine driver puts on every k8s bead.
const Label = "k8s"

// Capturer reads timelines from one bd serve.
type Capturer struct {
	Client *beads.Client
	// OwnerDepth bounds the owner-chain walk per touched object.
	OwnerDepth int
	// Logf receives progress lines; nil disables.
	Logf func(format string, args ...any)
}

func (c *Capturer) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}

// Head returns the highest kine revision currently stored (max mr over all
// k8s beads). It is a full listing; cheap on a local server, documented as a
// known cost. Journal `head` is a sequence number, not a revision, so it
// cannot stand in for this.
func (c *Capturer) Head(ctx context.Context) (int64, error) {
	return c.HeadWhere(ctx, nil)
}

// HeadWhere is Head restricted to beads whose key satisfies keep (nil keeps
// all). Settling uses it with the plan's background filter: node leases and
// friends renew every few seconds, so the unfiltered head never goes quiet
// on a live cluster.
func (c *Capturer) HeadWhere(ctx context.Context, keep func(key string) bool) (int64, error) {
	if keep == nil {
		return c.HeadWhereIssue(ctx, nil)
	}
	return c.HeadWhereIssue(ctx, func(is *beads.Issue) bool { return keep(is.Key()) })
}

// HeadWhereIssue is HeadWhere with the whole issue available to the filter
// (its id is needed to resolve ownership while settling on a subtree).
func (c *Capturer) HeadWhereIssue(ctx context.Context, keep func(is *beads.Issue) bool) (int64, error) {
	issues, err := c.Client.ListIssues(ctx, Label)
	if err != nil {
		return 0, err
	}
	var head int64
	for i := range issues {
		if keep != nil && !keep(&issues[i]) {
			continue
		}
		r, err := kine.ParseRec(issues[i].Description)
		if err != nil {
			continue
		}
		if r.ModRev > head {
			head = r.ModRev
		}
		if r.CreateRev > head {
			head = r.CreateRev
		}
	}
	return head, nil
}

// Capture builds a Run for the window using the requested source.
func (c *Capturer) Capture(ctx context.Context, rng Range, src Source) (*Run, error) {
	run := &Run{
		Version:    Version,
		Server:     c.Client.BaseURL,
		Range:      rng,
		CapturedAt: time.Now().UTC(),
		Owners:     map[string][]string{},
	}
	switch src {
	case SourceJournal, SourceAuto:
		err := c.captureJournal(ctx, run)
		if err == nil {
			break
		}
		if src == SourceJournal || !errors.Is(err, beads.ErrJournalDisabled) {
			return nil, err
		}
		run.Notes = append(run.Notes, "journal disabled on this workspace; fell back to the lossy snapshot source (enable with `bd config set events-journal true` on every writer, then restart kine-beads and bd serve)")
		c.logf("journal disabled; falling back to snapshot source")
		if err := c.captureSnapshot(ctx, run); err != nil {
			return nil, err
		}
	case SourceSnapshot:
		if err := c.captureSnapshot(ctx, run); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown source %q", src)
	}
	if run.Range.To == 0 {
		run.Range.To = run.Head
	}
	if err := c.resolveOwners(ctx, run); err != nil {
		return nil, err
	}
	Sort(run.Events)
	return run, nil
}

// captureSnapshot reconstructs the window from each bead's cr/mr.
func (c *Capturer) captureSnapshot(ctx context.Context, run *Run) error {
	run.Source = string(SourceSnapshot)
	run.Lossy = true
	issues, err := c.Client.ListIssues(ctx, Label)
	if err != nil {
		return err
	}
	c.logf("snapshot: %d k8s beads", len(issues))
	for i := range issues {
		is := &issues[i]
		r, err := kine.ParseRec(is.Description)
		if err != nil {
			continue
		}
		if r.ModRev > run.Head {
			run.Head = r.ModRev
		}
		key := is.Key()
		pk := kine.ParseKey(key)
		base := Event{Key: key, Kind: is.IssueType, Namespace: pk.Namespace, Name: pk.Name, BeadID: is.ID, Actor: "kine", CreateRev: r.CreateRev}
		// Two known points per object: its creation and its last write.
		if run.Range.Contains(r.CreateRev) {
			e := base
			e.Op, e.Rev, e.At = OpCreate, r.CreateRev, is.CreatedAt
			e.ID = "snap:" + is.ID + "@" + strconv.FormatInt(e.Rev, 10)
			run.Events = append(run.Events, e)
		}
		if r.ModRev != r.CreateRev && run.Range.Contains(r.ModRev) {
			e := base
			e.Op, e.Rev, e.At = OpUpdate, r.ModRev, is.UpdatedAt
			if r.Deleted {
				e.Op = OpDelete
			}
			e.ID = "snap:" + is.ID + "@" + strconv.FormatInt(e.Rev, 10)
			run.Events = append(run.Events, e)
		}
	}
	return nil
}

// captureJournal pages the events journal from seq 0 and keeps k8s records
// whose kine revision falls in the window.
func (c *Capturer) captureJournal(ctx context.Context, run *Run) error {
	run.Source = string(SourceJournal)
	run.Lossy = false
	idToKey := map[string]string{} // bead id -> etcd key, learned as records stream by
	idToKind := map[string]string{}
	var since int64
	pages := 0
	for {
		page, err := c.Client.Events(ctx, since, 10000)
		if err != nil {
			return err
		}
		pages++
		for _, rec := range page.Records {
			since = rec.Seq
			var is beads.Issue
			if len(rec.Issue) > 0 && string(rec.Issue) != "null" {
				if err := json.Unmarshal(rec.Issue, &is); err != nil {
					return fmt.Errorf("journal seq %d: decode issue: %w", rec.Seq, err)
				}
				if is.ID != "" {
					idToKey[is.ID] = is.Key()
					idToKind[is.ID] = is.IssueType
				}
			}
			switch rec.Op {
			case "create", "update", "close":
				if !is.HasLabel(Label) {
					continue
				}
				r, err := kine.ParseRec(is.Description)
				if err != nil {
					continue
				}
				if r.ModRev > run.Head {
					run.Head = r.ModRev
				}
				e := Event{
					ID: "seq:" + strconv.FormatInt(rec.Seq, 10), Seq: rec.Seq, Actor: rec.Actor, At: rec.TS,
					Key: is.Key(), Kind: is.IssueType, BeadID: is.ID, CreateRev: r.CreateRev,
				}
				pk := kine.ParseKey(e.Key)
				e.Namespace, e.Name = pk.Namespace, pk.Name
				switch {
				case rec.Op == "create":
					e.Op, e.Rev = OpCreate, r.CreateRev
				case r.Deleted:
					e.Op, e.Rev = OpDelete, r.ModRev
				default:
					e.Op, e.Rev = OpUpdate, r.ModRev
				}
				if run.Range.Contains(e.Rev) {
					run.Events = append(run.Events, e)
				}
			case "dep_add", "dep_remove":
				if rec.Dep == nil || rec.Dep.Kind != "parent-child" {
					continue
				}
				childKey, ok := idToKey[rec.IssueID]
				if !ok {
					childKey = c.lookupKey(ctx, rec.IssueID, idToKey, idToKind)
				}
				ownerKey, ok := idToKey[rec.Dep.Target]
				if !ok {
					ownerKey = c.lookupKey(ctx, rec.Dep.Target, idToKey, idToKind)
				}
				if childKey == "" || ownerKey == "" {
					continue
				}
				// Edge records carry no kine revision; they are placed at
				// the revision of the child's latest object event so far.
				rev := lastRevFor(run.Events, childKey)
				if rev == 0 || !run.Range.Contains(rev) {
					continue
				}
				op := OpDepAdd
				if rec.Op == "dep_remove" {
					op = OpDepRemove
				}
				pk := kine.ParseKey(childKey)
				run.Events = append(run.Events, Event{
					ID: "seq:" + strconv.FormatInt(rec.Seq, 10), Seq: rec.Seq, Rev: rev, Op: op, Actor: rec.Actor, At: rec.TS,
					Key: childKey, Kind: idToKind[rec.IssueID], Namespace: pk.Namespace, Name: pk.Name, BeadID: rec.IssueID, Owner: ownerKey,
				})
				if op == OpDepAdd {
					run.Owners[childKey] = appendUnique(run.Owners[childKey], ownerKey)
				}
			}
		}
		if len(page.Records) == 0 || since >= page.Head {
			break
		}
	}
	c.logf("journal: %d pages, head seq %d, %d events in %s", pages, since, len(run.Events), run.Range)
	return nil
}

func (c *Capturer) lookupKey(ctx context.Context, id string, idToKey, idToKind map[string]string) string {
	is, err := c.Client.GetIssue(ctx, id)
	if err != nil {
		return ""
	}
	idToKey[id] = is.Key()
	idToKind[id] = is.IssueType
	return is.Key()
}

func lastRevFor(events []Event, key string) int64 {
	var rev int64
	for i := range events {
		if events[i].Key == key && events[i].Op.IsObjectOp() && events[i].Rev > rev {
			rev = events[i].Rev
		}
	}
	return rev
}

func appendUnique(xs []string, x string) []string {
	for _, y := range xs {
		if y == x {
			return xs
		}
	}
	return append(xs, x)
}

// resolveOwners fills run.Owners for every key an event touched by walking
// the owner chain (tree direction=down from the owned object) in beads.
func (c *Capturer) resolveOwners(ctx context.Context, run *Run) error {
	depth := c.OwnerDepth
	if depth <= 0 {
		depth = 16
	}
	seen := map[string]bool{}
	for _, e := range run.Events {
		if seen[e.Key] || e.BeadID == "" {
			continue
		}
		seen[e.Key] = true
		chain, err := c.OwnerChain(ctx, e.BeadID, depth)
		if err != nil {
			// A deleted bead may be gone; ownership from the journal (dep_add) stands.
			if run.Owners[e.Key] == nil {
				run.Owners[e.Key] = []string{}
			}
			continue
		}
		if len(chain) == 0 {
			chain = run.Owners[e.Key]
		}
		if chain == nil {
			chain = []string{}
		}
		run.Owners[e.Key] = chain
	}
	return nil
}

// OwnerChain returns the owner keys of a bead, nearest first, by walking
// the dependency tree "down" from the owned object (child depends on
// parent) and following parent_id links from the root row.
func (c *Capturer) OwnerChain(ctx context.Context, beadID string, depth int) ([]string, error) {
	if depth <= 0 {
		depth = 16
	}
	nodes, err := c.Client.Tree(ctx, beadID, "down", depth)
	if err != nil {
		return nil, err
	}
	byID := map[string]beads.TreeNode{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	chain := []string{}
	cur := beadID
	for i := 0; i < depth; i++ {
		next := ""
		for _, n := range nodes {
			if n.ParentID == cur && n.Depth > 0 && !n.Deduped {
				next = n.ID
				break
			}
		}
		if next == "" {
			break
		}
		n := byID[next]
		chain = append(chain, n.Key())
		cur = next
	}
	return chain, nil
}
