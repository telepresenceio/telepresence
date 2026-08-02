package state

import (
	"bytes"
	"testing"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/manifest"
)

// validate renders s and loads it through the product's own manifest
// loader (pkg/client/cli/manifest.Load), which validates against the
// embedded state.schema.yaml before decoding: a builder/schema mismatch
// fails here rather than surfacing later as an opaque `telepresence apply`
// error in a suite.
func validate(t *testing.T, s *State) {
	t.Helper()
	data, err := s.Render()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if _, err := manifest.Load(bytes.NewReader(data)); err != nil {
		t.Fatalf("product loader rejected generated manifest:\n%s\nerror: %v", data, err)
	}
}

// TestRender_AttachmentTypes generates a minimal manifest for each
// attachment type and validates it, one per Type.
func TestRender_AttachmentTypes(t *testing.T) {
	tests := []struct {
		name string
		att  func() Attachment
	}{
		{"intercept", func() Attachment {
			a := Intercept("echo-easy")
			a.Ports = []string{"8080:http"}
			return a
		}},
		{"replace", func() Attachment {
			a := Replace("echo-server")
			a.Ports = []string{"all"}
			return a
		}},
		{"ingest", func() Attachment {
			return Ingest("echo-sidecar")
		}},
		{"wiretap", func() Attachment {
			a := Wiretap("echo-tap")
			a.Workload = "echo-server"
			return a
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			validate(t, &State{Attachments: []Attachment{tt.att()}})
		})
	}
}

// TestRender_Full exercises every Connection and Attachment field (mirroring
// pkg/client/cli/manifest/testdata/valid-full.yaml) built through the typed
// API, to catch a json-tag mismatch none of the minimal per-type cases
// above would trigger.
func TestRender_Full(t *testing.T) {
	intercept := Intercept("echo-easy")
	intercept.Ports = []string{"8080:http", "9090"}
	intercept.Service = "echo-easy"
	intercept.HTTPHeaders = []string{"x-dev-user=thomas"}
	intercept.Metadata = map[string]string{"owner": "thhal"}
	intercept.ToPod = []string{"8081/UDP", "9091"}
	intercept.Env = &Env{File: "./echo.env", Syntax: "sh:export"}
	intercept.Mount = &Mount{Path: "/tmp/echo-mounts", ReadOnly: true}

	replace := Replace("echo-server/svc")
	replace.Ports = []string{"all"}

	ingest := Ingest("echo-sidecar")
	ingest.Container = "logger"
	ingest.Mount = &Mount{LocalMountPort: 1234}

	wiretap := Wiretap("echo-tap")
	wiretap.Workload = "echo-server"
	wiretap.Plaintext = true

	s := &State{
		Connection: &Connection{
			Name:             "dev",
			Context:          "kind-dev",
			Namespace:        "default",
			ManagerNamespace: "ambassador",
			MappedNamespaces: []string{"default", "backend"},
			AlsoProxy:        []string{"10.96.0.0/12"},
			NeverProxy:       []string{"2001:db8::/64"},
			Vnat:             []string{"10.101.0.0/16", "service"},
			ProxyVia: []ProxyVia{
				{Subnet: "pods", Workload: "echo-server"},
				{Subnet: "10.100.0.0/16", Workload: "local"},
			},
			RerouteLocal: []string{"8080:my-svc:http/tcp"},
			KubeFlags:    map[string]string{"request-timeout": "30s"},
		},
		Attachments: []Attachment{intercept, replace, ingest, wiretap},
	}
	validate(t, s)
}

// TestRender_ConnectionOnly covers a manifest that declares only a
// connection, no attachments -- the schema's anyOf[connection,attachments]
// allows either alone.
func TestRender_ConnectionOnly(t *testing.T) {
	validate(t, &State{Connection: &Connection{Namespace: "default"}})
}

// TestBool covers the *bool helper the *bool fields (Attachment.NodeAgent,
// Mount.Enabled) need.
func TestBool(t *testing.T) {
	b := Bool(true)
	if b == nil || !*b {
		t.Fatalf("Bool(true) = %v, want a pointer to true", b)
	}
}
