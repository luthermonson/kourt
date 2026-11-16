package kine

import "testing"

func TestParseRec(t *testing.T) {
	r, err := ParseRec(`{"v":"azhz","cr":53862,"mr":53874,"del":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if r.CreateRev != 53862 || r.ModRev != 53874 || !r.Deleted || string(r.Value) != "k8s" {
		t.Fatalf("unexpected rec: %+v", r)
	}
	if _, err := ParseRec("not a record"); err == nil {
		t.Fatal("expected error for prose description")
	}
	if _, err := ParseRec(`{"foo":1}`); err == nil {
		t.Fatal("expected error for JSON without cr/mr")
	}
}

func TestParseKey(t *testing.T) {
	cases := []struct {
		raw                             string
		group, resource, namespace, name string
	}{
		{"/registry/pods/default/podinfo-1", "", "pods", "default", "podinfo-1"},
		{"/registry/namespaces/default", "", "namespaces", "", "default"},
		{"/registry/services/specs/default/podinfo", "", "services/specs", "default", "podinfo"},
		{"/registry/apiextensions.k8s.io/customresourcedefinitions/foo.example.com", "apiextensions.k8s.io", "customresourcedefinitions", "", "foo.example.com"},
		{"/registry/kyverno.io/policies/default/p1", "kyverno.io", "policies", "default", "p1"},
		{"not-a-key", "", "", "", ""},
	}
	for _, c := range cases {
		k := ParseKey(c.raw)
		if k.Group != c.group || k.Resource != c.resource || k.Namespace != c.namespace || k.Name != c.name {
			t.Errorf("%s: got %+v", c.raw, k)
		}
	}
}

func TestNormalize(t *testing.T) {
	if Normalize("pods/default/x") != "/registry/pods/default/x" {
		t.Fatal("short ref not normalized")
	}
	if Normalize("/registry/pods/default/x") != "/registry/pods/default/x" {
		t.Fatal("full key altered")
	}
	if Short("/registry/pods/a/b") != "pods/a/b" {
		t.Fatal("short failed")
	}
}
