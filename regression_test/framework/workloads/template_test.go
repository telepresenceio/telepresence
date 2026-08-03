package workloads

import (
	"bufio"
	"io"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/yaml"
	sigsyaml "sigs.k8s.io/yaml"
)

// renderDocs renders tpl in ns and decodes each YAML document into a generic
// map, the same document boundaries `kubectl apply -f` would see.
func renderDocs(t *testing.T, tpl Template, ns string) []map[string]any {
	t.Helper()
	manifest, err := tpl.Render(ns)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	r := yaml.NewYAMLReader(bufio.NewReader(strings.NewReader(manifest)))
	var docs []map[string]any
	for {
		raw, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("split manifest into documents:\n%s\nerror: %v", manifest, err)
		}
		var doc map[string]any
		if err := sigsyaml.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("unmarshal document:\n%s\nerror: %v", raw, err)
		}
		docs = append(docs, doc)
	}
	return docs
}

// workloadDoc returns the document whose kind matches tpl.Kind, failing the
// test if none is found.
func workloadDoc(t *testing.T, docs []map[string]any, kind string) map[string]any {
	t.Helper()
	for _, d := range docs {
		if d["kind"] == kind {
			return d
		}
	}
	t.Fatalf("no %q document among %d rendered", kind, len(docs))
	return nil
}

// containers returns spec.template.spec.containers of a workload document.
func containers(t *testing.T, doc map[string]any) []any {
	t.Helper()
	spec, _ := doc["spec"].(map[string]any)
	tmpl, _ := spec["template"].(map[string]any)
	podSpec, _ := tmpl["spec"].(map[string]any)
	cs, _ := podSpec["containers"].([]any)
	if cs == nil {
		t.Fatalf("no spec.template.spec.containers in document: %v", doc)
	}
	return cs
}

// TestRender_ReplicaSet checks that EchoReplicaSet renders a bare
// apps/v1 ReplicaSet: same selector/template shape as Echo's Deployment,
// just a different kind.
func TestRender_ReplicaSet(t *testing.T) {
	docs := renderDocs(t, EchoReplicaSet("rs-echo"), "rtest-ns")
	doc := workloadDoc(t, docs, "ReplicaSet")
	if doc["apiVersion"] != "apps/v1" {
		t.Fatalf("apiVersion = %v, want apps/v1", doc["apiVersion"])
	}
	if _, ok := doc["spec"].(map[string]any)["strategy"]; ok {
		t.Fatalf("ReplicaSet document unexpectedly has a strategy field: %v", doc)
	}
	cs := containers(t, doc)
	if len(cs) != 1 {
		t.Fatalf("len(containers) = %d, want 1", len(cs))
	}
}

// TestRender_Rollout checks that EchoRollout renders an Argo Rollout with an
// empty canary strategy.
func TestRender_Rollout(t *testing.T) {
	docs := renderDocs(t, EchoRollout("ro-echo"), "rtest-ns")
	doc := workloadDoc(t, docs, "Rollout")
	if doc["apiVersion"] != "argoproj.io/v1alpha1" {
		t.Fatalf("apiVersion = %v, want argoproj.io/v1alpha1", doc["apiVersion"])
	}
	strategy, _ := doc["spec"].(map[string]any)["strategy"].(map[string]any)
	if strategy == nil {
		t.Fatalf("spec.strategy missing: %v", doc)
	}
	if _, ok := strategy["canary"]; !ok {
		t.Fatalf("spec.strategy.canary missing: %v", strategy)
	}
}

// TestRender_Env checks that Template.Env renders as container env vars, and
// that rendering the same template twice is byte-identical: text/template
// sorts map ranges by key, so the manifest must not vary run to run.
func TestRender_Env(t *testing.T) {
	tpl := Echo("env-echo")
	tpl.Env = map[string]string{"ZEBRA": "z", "ALPHA": "a"}
	m1, err := tpl.Render("rtest-ns")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	m2, err := tpl.Render("rtest-ns")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if m1 != m2 {
		t.Fatalf("render is not deterministic:\n%s\n---\n%s", m1, m2)
	}
	if strings.Index(m1, "ALPHA") > strings.Index(m1, "ZEBRA") {
		t.Fatalf("Env not rendered in sorted key order:\n%s", m1)
	}
	docs := renderDocs(t, tpl, "rtest-ns")
	doc := workloadDoc(t, docs, "Deployment")
	cs := containers(t, doc)
	app, _ := cs[0].(map[string]any)
	env, _ := app["env"].([]any)
	if !envHas(env, "ALPHA", "a") || !envHas(env, "ZEBRA", "z") {
		t.Fatalf("app container env missing declared vars: %v", env)
	}
}

// TestRender_ExtraContainers checks that ExtraContainers renders as
// additional pod containers, each with its own PORTS env var, declared Env,
// and (when set) its own ConfigMap and read-only mount distinct from the
// app container's own ConfigVolume.
func TestRender_ExtraContainers(t *testing.T) {
	tpl := EchoReplicaSet("extra-echo")
	tpl.ConfigVolume = ConfigVolume{
		Name: "extra-echo-config", Key: "app.conf", Content: "app-marker", MountPath: "/etc/app",
	}
	tpl.ExtraContainers = []ExtraContainer{{
		Name: "sidecar",
		Port: 9090,
		Env:  map[string]string{"ROLE": "sidecar"},
		ConfigVolume: ConfigVolume{
			Name: "extra-echo-sidecar-config", Key: "sidecar.conf", Content: "sidecar-marker", MountPath: "/etc/sidecar",
		},
	}}
	docs := renderDocs(t, tpl, "rtest-ns")

	doc := workloadDoc(t, docs, "ReplicaSet")
	cs := containers(t, doc)
	if len(cs) != 2 {
		t.Fatalf("len(containers) = %d, want 2 (app + sidecar): %v", len(cs), cs)
	}
	sidecar, _ := cs[1].(map[string]any)
	if sidecar["name"] != "sidecar" {
		t.Fatalf("containers[1].name = %v, want sidecar", sidecar["name"])
	}
	ports, _ := sidecar["ports"].([]any)
	if len(ports) != 1 || ports[0].(map[string]any)["containerPort"] != float64(9090) {
		t.Fatalf("sidecar ports = %v, want [{containerPort: 9090}]", ports)
	}
	env, _ := sidecar["env"].([]any)
	if !envHas(env, "PORTS", "9090") || !envHas(env, "ROLE", "sidecar") {
		t.Fatalf("sidecar env missing PORTS/ROLE: %v", env)
	}
	mounts, _ := sidecar["volumeMounts"].([]any)
	if len(mounts) != 1 || mounts[0].(map[string]any)["mountPath"] != "/etc/sidecar" {
		t.Fatalf("sidecar volumeMounts = %v, want a single /etc/sidecar mount", mounts)
	}

	var configMaps int
	for _, d := range docs {
		if d["kind"] == "ConfigMap" {
			configMaps++
		}
	}
	if configMaps != 2 {
		t.Fatalf("ConfigMap documents = %d, want 2 (app + sidecar)", configMaps)
	}
}

// envHas reports whether env (a decoded container env list) declares name
// with value.
func envHas(env []any, name, value string) bool {
	for _, e := range env {
		m, _ := e.(map[string]any)
		if m["name"] == name {
			v, _ := m["value"].(string)
			return v == value
		}
	}
	return false
}
