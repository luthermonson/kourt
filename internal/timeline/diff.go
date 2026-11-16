package timeline

import (
	"sort"
)

// Change summarises what happened to one object across a run.
type Change struct {
	Key      string `json:"key"`
	Kind     string `json:"kind"`
	BeadID   string `json:"bead_id"`
	Change   string `json:"change"` // created | updated | deleted | created+deleted | edges
	FirstRev int64  `json:"first_rev"`
	LastRev  int64  `json:"last_rev"`
	Events   int    `json:"events"`
	Creates  int    `json:"creates"`
	Updates  int    `json:"updates"`
	Deletes  int    `json:"deletes"`
	Edges    int    `json:"edges"`
	// Owners is the owner chain (nearest first) as captured.
	Owners []string `json:"owners,omitempty"`
}

// Changeset is the per-object view of a run: `litecone diff`'s answer.
type Changeset struct {
	Range   Range    `json:"range"`
	Source  string   `json:"source"`
	Lossy   bool     `json:"lossy"`
	Objects []Change `json:"objects"`
	Totals  struct {
		Objects int `json:"objects"`
		Events  int `json:"events"`
		Creates int `json:"creates"`
		Updates int `json:"updates"`
		Deletes int `json:"deletes"`
	} `json:"totals"`
	Notes []string `json:"notes,omitempty"`
}

// Diff folds a run's events into one row per object.
func Diff(run *Run) *Changeset {
	cs := &Changeset{Range: run.Range, Source: run.Source, Lossy: run.Lossy, Notes: run.Notes}
	byKey := map[string]*Change{}
	for _, e := range run.Events {
		c, ok := byKey[e.Key]
		if !ok {
			c = &Change{Key: e.Key, Kind: e.Kind, BeadID: e.BeadID, FirstRev: e.Rev, LastRev: e.Rev, Owners: run.Owners[e.Key]}
			byKey[e.Key] = c
		}
		c.Events++
		if e.Rev < c.FirstRev {
			c.FirstRev = e.Rev
		}
		if e.Rev > c.LastRev {
			c.LastRev = e.Rev
		}
		switch e.Op {
		case OpCreate:
			c.Creates++
		case OpUpdate:
			c.Updates++
		case OpDelete:
			c.Deletes++
		default:
			c.Edges++
		}
	}
	for _, c := range byKey {
		switch {
		case c.Creates > 0 && c.Deletes > 0:
			c.Change = "created+deleted"
		case c.Deletes > 0:
			c.Change = "deleted"
		case c.Creates > 0:
			c.Change = "created"
		case c.Updates > 0:
			c.Change = "updated"
		default:
			c.Change = "edges"
		}
		cs.Objects = append(cs.Objects, *c)
		cs.Totals.Objects++
		cs.Totals.Events += c.Events
		cs.Totals.Creates += c.Creates
		cs.Totals.Updates += c.Updates
		cs.Totals.Deletes += c.Deletes
	}
	sort.Slice(cs.Objects, func(i, j int) bool {
		if cs.Objects[i].FirstRev != cs.Objects[j].FirstRev {
			return cs.Objects[i].FirstRev < cs.Objects[j].FirstRev
		}
		return cs.Objects[i].Key < cs.Objects[j].Key
	})
	return cs
}
