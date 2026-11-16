// Package beads is a minimal net/http client for the beads /v0 HTTP API
// (`bd serve`). It deliberately does not import the beads Go module: the
// binary must stay self-contained and cross-platform, and the /v0 surface is
// the contract gas city itself consumes.
//
// Routes used (verified against bd 1.3.4, wire_revision 2):
//
//	GET /v0/beads/context                               handshake + capabilities
//	GET /v0/beads/issues?label=k8s&limit=0[&cursor=]    full listing (limit=0 = unlimited)
//	GET /v0/beads/issues/{id}                           one issue
//	GET /v0/beads/dependencies?issue_id={id}            edges of one issue
//	GET /v0/beads/dependencies/tree?root_id=&direction=up|down|both&max_depth=
//	GET /v0/beads/events?since=N&limit=M                the events journal (409 when disabled)
//
// Errors are RFC 7807-style problem documents: {code, detail, status, title, param?}.
package beads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client talks to one bd serve instance.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	// Token is sent as a bearer token when set (bd serve --auth-token-file).
	Token string
}

// New returns a client for base (e.g. http://127.0.0.1:8099).
func New(base string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(base, "/"),
		HTTP:    &http.Client{Timeout: 120 * time.Second},
	}
}

// Problem is the error document bd serve answers with.
type Problem struct {
	Code      string `json:"code"`
	Detail    string `json:"detail"`
	Status    int    `json:"status"`
	Title     string `json:"title"`
	Param     string `json:"param,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

func (p *Problem) Error() string {
	if p.Param != "" {
		return fmt.Sprintf("beads: %d %s (%s, param %s): %s", p.Status, p.Title, p.Code, p.Param, p.Detail)
	}
	return fmt.Sprintf("beads: %d %s (%s): %s", p.Status, p.Title, p.Code, p.Detail)
}

// ErrJournalDisabled is returned by Events when the workspace has not enabled
// the durable events journal (`bd config set events-journal true` on every
// writer, then restart bd serve and the kine-beads process).
var ErrJournalDisabled = errors.New("beads events journal is disabled on this workspace")

// Context is the /v0/beads/context handshake.
type Context struct {
	APIVersion   string   `json:"api_version"`
	Backend      string   `json:"backend"`
	BDVersion    string   `json:"bd_version"`
	BeadsDir     string   `json:"beads_dir"`
	Capabilities []string `json:"capabilities"`
	Database     string   `json:"database"`
	DoltMode     string   `json:"dolt_mode"`
	WireRevision int      `json:"wire_revision"`
}

// Has reports whether the server advertises a capability.
func (c *Context) Has(cap string) bool {
	for _, x := range c.Capabilities {
		if x == cap {
			return true
		}
	}
	return false
}

// Issue is the subset of the issue wire shape litecone reads.
type Issue struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Status      string   `json:"status"`
	IssueType   string   `json:"issue_type"`
	ExternalRef string   `json:"external_ref"`
	Labels      []string `json:"labels"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

// Key is the etcd key this bead mirrors (external_ref, falling back to title).
func (i *Issue) Key() string {
	if i.ExternalRef != "" {
		return i.ExternalRef
	}
	return i.Title
}

// HasLabel reports whether the issue carries a label.
func (i *Issue) HasLabel(l string) bool {
	for _, x := range i.Labels {
		if x == l {
			return true
		}
	}
	return false
}

// TreeNode is one row of /dependencies/tree.
type TreeNode struct {
	Issue
	Depth          int    `json:"depth"`
	ParentID       string `json:"parent_id"`
	EdgeFromParent string `json:"edge_from_parent,omitempty"`
	Truncated      bool   `json:"truncated"`
	Deduped        bool   `json:"deduped,omitempty"`
}

// Dependency is one edge. For ownerReferences the kine driver writes
// issue_id = child (owned object), depends_on_id = parent (owner),
// type = parent-child.
type Dependency struct {
	IssueID     string `json:"issue_id"`
	DependsOnID string `json:"depends_on_id"`
	Type        string `json:"type"`
	CreatedAt   string `json:"created_at"`
	CreatedBy   string `json:"created_by"`
}

// EventRecord is one journal record (eventsjournal.Record upstream).
type EventRecord struct {
	Seq     int64           `json:"seq"`
	TS      string          `json:"ts"`
	Op      string          `json:"op"` // create, update, close, delete, dep_add, dep_remove, comment
	IssueID string          `json:"issue_id"`
	Actor   string          `json:"actor,omitempty"`
	Issue   json.RawMessage `json:"issue"` // full issue AFTER the mutation; literal null on delete
	Dep     *struct {
		Kind     string `json:"kind"`
		Target   string `json:"target"`
		Metadata string `json:"metadata,omitempty"`
	} `json:"dep,omitempty"`
}

// EventsPage is one page of the journal.
type EventsPage struct {
	Head    int64         `json:"head"`
	Records []EventRecord `json:"records"`
}

type issuePage struct {
	HasMore    bool    `json:"has_more"`
	Items      []Issue `json:"items"`
	NextCursor string  `json:"next_cursor"`
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	u := c.BaseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("beads: GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("beads: read %s: %w", path, err)
	}
	if resp.StatusCode/100 != 2 {
		var p Problem
		if json.Unmarshal(body, &p) == nil && p.Code != "" {
			if p.Code == "events_journal_disabled" {
				return fmt.Errorf("%w: %s", ErrJournalDisabled, p.Detail)
			}
			return &p
		}
		return fmt.Errorf("beads: GET %s: HTTP %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("beads: decode %s: %w", path, err)
	}
	return nil
}

// Context performs the handshake.
func (c *Client) Context(ctx context.Context) (*Context, error) {
	var out Context
	if err := c.get(ctx, "/v0/beads/context", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListIssues returns every issue carrying label (limit=0 means unlimited on
// this route; the cursor is followed anyway in case the server pages).
func (c *Client) ListIssues(ctx context.Context, label string) ([]Issue, error) {
	var all []Issue
	cursor := ""
	for {
		q := url.Values{}
		q.Set("limit", "0")
		if label != "" {
			q.Set("label", label)
		}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var page issuePage
		if err := c.get(ctx, "/v0/beads/issues", q, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Items...)
		if !page.HasMore || page.NextCursor == "" || page.NextCursor == cursor {
			return all, nil
		}
		cursor = page.NextCursor
	}
}

// GetIssue fetches one issue by id.
func (c *Client) GetIssue(ctx context.Context, id string) (*Issue, error) {
	var out Issue
	if err := c.get(ctx, "/v0/beads/issues/"+url.PathEscape(id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// FindByKey looks an issue up by its etcd key (title filter; exact match on
// external_ref/title is applied client-side).
func (c *Client) FindByKey(ctx context.Context, key string) (*Issue, error) {
	q := url.Values{}
	q.Set("title", key)
	q.Set("limit", "20")
	var page issuePage
	if err := c.get(ctx, "/v0/beads/issues", q, &page); err != nil {
		return nil, err
	}
	for i := range page.Items {
		if page.Items[i].Key() == key {
			return &page.Items[i], nil
		}
	}
	return nil, fmt.Errorf("beads: no bead for key %s", key)
}

// Dependencies lists the edges of one issue.
func (c *Client) Dependencies(ctx context.Context, id string) ([]Dependency, error) {
	q := url.Values{}
	q.Set("issue_id", id)
	var out struct {
		Items []Dependency `json:"items"`
	}
	if err := c.get(ctx, "/v0/beads/dependencies", q, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// Tree walks the dependency tree from root. For ownership edges:
//
//	direction=up   from an OWNER: the objects it owns, transitively (Deployment -> RS -> Pods)
//	direction=down from an OWNED object: its owner chain (Pod -> RS -> Deployment)
func (c *Client) Tree(ctx context.Context, root, direction string, maxDepth int) ([]TreeNode, error) {
	q := url.Values{}
	q.Set("root_id", root)
	q.Set("direction", direction)
	if maxDepth > 0 {
		q.Set("max_depth", strconv.Itoa(maxDepth))
	}
	var out struct {
		Items []TreeNode `json:"items"`
	}
	if err := c.get(ctx, "/v0/beads/dependencies/tree", q, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// Events reads one page of the journal with seq > since. ErrJournalDisabled
// (wrapped) is returned when the workspace has the journal off.
func (c *Client) Events(ctx context.Context, since int64, limit int) (*EventsPage, error) {
	q := url.Values{}
	q.Set("since", strconv.FormatInt(since, 10))
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out EventsPage
	if err := c.get(ctx, "/v0/beads/events", q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// JournalEnabled probes the journal once. It returns false, nil when the
// server speaks events.list but the workspace has it disabled.
func (c *Client) JournalEnabled(ctx context.Context) (bool, error) {
	_, err := c.Events(ctx, 0, 1)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, ErrJournalDisabled) {
		return false, nil
	}
	return false, err
}
