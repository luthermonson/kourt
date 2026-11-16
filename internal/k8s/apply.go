// Package k8s applies and deletes plan inputs through the apiserver using
// client-go (server-side apply via the dynamic client). No kubectl.
package k8s

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
)

// FieldManager is the server-side-apply manager name litecone writes under.
// When per-controller provenance lands in the substrate, this is the name
// the stimulus will show up as.
const FieldManager = "litecone"

// Client wraps a dynamic client with a REST mapper.
type Client struct {
	dyn    dynamic.Interface
	mapper meta.RESTMapper
}

// Connect builds a client from a kubeconfig path ("" uses the default
// loading rules: $KUBECONFIG, then ~/.kube/config).
func Connect(kubeconfig string) (*Client, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("kubeconfig: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, err
	}
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(dc))
	return &Client{dyn: dyn, mapper: mapper}, nil
}

// Object identifies one applied/deleted object as the timeline will see it.
type Object struct {
	GVK       schema.GroupVersionKind
	Resource  string
	Namespace string
	Name      string
	// Key is the etcd key the apiserver will use, best-effort: the plural
	// resource for core/apps kinds matches kine's /registry/<resource>/...
	// layout; grouped resources are prefixed with the group.
	Key string
}

// Load reads a multi-document YAML/JSON manifest file.
func Load(path string) ([]*unstructured.Unstructured, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(b), 4096)
	var objs []*unstructured.Unstructured
	for {
		var u unstructured.Unstructured
		if err := dec.Decode(&u); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if len(u.Object) == 0 {
			continue
		}
		objs = append(objs, &u)
	}
	return objs, nil
}

func (c *Client) resourceFor(u *unstructured.Unstructured) (dynamic.ResourceInterface, Object, error) {
	gvk := u.GroupVersionKind()
	m, err := c.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, Object{}, fmt.Errorf("%s: %w", gvk, err)
	}
	obj := Object{GVK: gvk, Resource: m.Resource.Resource, Namespace: u.GetNamespace(), Name: u.GetName()}
	var ri dynamic.ResourceInterface
	if m.Scope.Name() == meta.RESTScopeNameNamespace {
		if obj.Namespace == "" {
			obj.Namespace = "default"
		}
		ri = c.dyn.Resource(m.Resource).Namespace(obj.Namespace)
	} else {
		ri = c.dyn.Resource(m.Resource)
	}
	obj.Key = etcdKey(m.Resource, obj.Namespace, obj.Name)
	return ri, obj, nil
}

// etcdKey guesses the registry key for an object. The apiserver uses the
// plural resource for most kinds but has legacy exceptions (nodes are
// "minions", services split into services/specs). This is display/help only;
// the gate matches on what the timeline actually recorded.
func etcdKey(gvr schema.GroupVersionResource, ns, name string) string {
	res := gvr.Resource
	switch {
	case gvr.Group == "" && res == "nodes":
		res = "minions"
	case gvr.Group == "" && res == "services":
		res = "services/specs"
	case gvr.Group != "" && gvr.Group != "apps" && gvr.Group != "batch" && gvr.Group != "extensions" &&
		gvr.Group != "rbac.authorization.k8s.io" && gvr.Group != "policy" && gvr.Group != "autoscaling" &&
		gvr.Group != "networking.k8s.io" && gvr.Group != "storage.k8s.io" && gvr.Group != "coordination.k8s.io":
		res = gvr.Group + "/" + res
	}
	if ns != "" {
		return "/registry/" + res + "/" + ns + "/" + name
	}
	return "/registry/" + res + "/" + name
}

// Apply server-side-applies every object in the manifest (force=true, like
// `kubectl apply --server-side --force-conflicts`).
func (c *Client) Apply(ctx context.Context, path string) ([]Object, error) {
	objs, err := Load(path)
	if err != nil {
		return nil, err
	}
	var out []Object
	for _, u := range objs {
		ri, obj, err := c.resourceFor(u)
		if err != nil {
			return out, err
		}
		data, err := u.MarshalJSON()
		if err != nil {
			return out, err
		}
		force := true
		if _, err := ri.Patch(ctx, u.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{FieldManager: FieldManager, Force: &force}); err != nil {
			return out, fmt.Errorf("apply %s %s/%s: %w", obj.GVK.Kind, obj.Namespace, obj.Name, err)
		}
		out = append(out, obj)
	}
	return out, nil
}

// Delete deletes every object named in the manifest. Missing objects are
// not an error.
func (c *Client) Delete(ctx context.Context, path string) ([]Object, error) {
	objs, err := Load(path)
	if err != nil {
		return nil, err
	}
	var out []Object
	for _, u := range objs {
		ri, obj, err := c.resourceFor(u)
		if err != nil {
			return out, err
		}
		if err := ri.Delete(ctx, u.GetName(), metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return out, fmt.Errorf("delete %s %s/%s: %w", obj.GVK.Kind, obj.Namespace, obj.Name, err)
		}
		out = append(out, obj)
	}
	return out, nil
}
