// Package kine decodes the k3s-on-beads object model: one etcd key is one
// bead, and the bead's description carries the kine record for that key.
//
// The shape is owned by the kine beads driver (kine/pkg/drivers/beads):
//
//	description = {"v": base64(protobuf), "cr": createRev, "mr": modRev, "lease": n, "del": bool}
//	external_ref = title = the etcd key, e.g. /registry/pods/default/podinfo-abc
//	issue_type   = the resource segment of the key (pods, deployments, ...)
//
// Deep decoding of "v" (the k8s protobuf value) is deliberately out of scope:
// litecone operates at the object + revision + event level.
package kine

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Rec is the kine record stored in a bead's description.
type Rec struct {
	Value     []byte `json:"v,omitempty"`
	CreateRev int64  `json:"cr"`
	ModRev    int64  `json:"mr"`
	Lease     int64  `json:"lease,omitempty"`
	Deleted   bool   `json:"del,omitempty"`
}

// ParseRec decodes a bead description. A description that is not a kine
// record (not JSON, or JSON without cr/mr) is an error so callers can skip
// non-k8s beads explicitly.
func ParseRec(description string) (Rec, error) {
	var r Rec
	s := strings.TrimSpace(description)
	if s == "" || s[0] != '{' {
		return r, fmt.Errorf("description is not a kine record")
	}
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		return r, fmt.Errorf("decode kine record: %w", err)
	}
	if r.ModRev == 0 && r.CreateRev == 0 {
		return r, fmt.Errorf("description has no cr/mr")
	}
	return r, nil
}

// Key is a parsed etcd registry key.
type Key struct {
	Raw       string
	Group     string // non-empty for aggregated/CRD keys like /registry/apiextensions.k8s.io/...
	Resource  string // pods, deployments, services/specs, ...
	Namespace string // empty for cluster-scoped objects
	Name      string
}

// Prefix is the etcd key prefix the apiserver writes under.
const Prefix = "/registry/"

// ParseKey splits an etcd key into its parts. The heuristics mirror how the
// apiserver lays keys out:
//
//	/registry/<resource>/<name>                       cluster-scoped
//	/registry/<resource>/<ns>/<name>                  namespaced
//	/registry/<resource>/<sub>/<ns>/<name>            e.g. services/specs, services/endpoints
//	/registry/<group>/<resource>/<name>               cluster-scoped, grouped (first segment has a dot)
//	/registry/<group>/<resource>/<ns>/<name>          namespaced, grouped
//
// Anything else keeps Raw and fills what it can. Matching in the gate is done
// on the raw key, so an imperfect parse only affects display.
func ParseKey(raw string) Key {
	k := Key{Raw: raw}
	rest := strings.TrimPrefix(raw, Prefix)
	if rest == raw {
		return k
	}
	seg := strings.Split(rest, "/")
	if len(seg) > 0 && strings.Contains(seg[0], ".") {
		k.Group = seg[0]
		seg = seg[1:]
	}
	switch len(seg) {
	case 0:
	case 1:
		k.Resource = seg[0]
	case 2:
		k.Resource, k.Name = seg[0], seg[1]
	case 3:
		k.Resource, k.Namespace, k.Name = seg[0], seg[1], seg[2]
	default:
		k.Resource = strings.Join(seg[:len(seg)-2], "/")
		k.Namespace = seg[len(seg)-2]
		k.Name = seg[len(seg)-1]
	}
	return k
}

// Normalize turns a short object reference as written in a plan
// (pods/default/podinfo-*) into the key form the timeline carries
// (/registry/pods/default/podinfo-*). Keys that already carry the prefix are
// returned unchanged.
func Normalize(ref string) string {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, Prefix) {
		return ref
	}
	return Prefix + strings.TrimPrefix(ref, "/")
}

// Short strips the registry prefix for display.
func Short(key string) string {
	return strings.TrimPrefix(key, Prefix)
}
