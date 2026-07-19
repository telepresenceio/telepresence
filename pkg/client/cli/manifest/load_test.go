package manifest_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/manifest"
)

func TestLoadFile_Valid(t *testing.T) {
	tests := []struct {
		name string
		file string
		want *manifest.State
	}{
		{
			name: "full manifest",
			file: "testdata/valid-full.yaml",
			want: &manifest.State{
				APIVersion: "telepresence.io/v1alpha1",
				Kind:       "WorkstationState",
				Connection: &manifest.Connection{
					Name:             "dev",
					Context:          "kind-dev",
					Namespace:        "default",
					ManagerNamespace: "ambassador",
					MappedNamespaces: []string{"default", "backend"},
					AlsoProxy:        []string{"10.96.0.0/12"},
					NeverProxy:       []string{"2001:db8::/64"},
					ProxyVia: []manifest.ProxyVia{
						{Subnet: "pods", Workload: "echo-server"},
						{Subnet: "10.100.0.0/16", Workload: "local"},
					},
					RerouteLocal: []string{"8080:my-svc:http/tcp"},
					KubeFlags:    map[string]string{"request-timeout": "30s"},
				},
				Attachments: []manifest.Attachment{
					{
						Type:        manifest.TypeIntercept,
						Name:        "echo-easy",
						Ports:       []manifest.PortIdentifier{"8080:http", "9090"},
						Service:     "echo-easy",
						HTTPHeaders: []string{"x-dev-user=thomas"},
						Metadata:    map[string]string{"owner": "thhal"},
						ToPod:       []string{"8081/UDP", "9091"},
						Env: &manifest.Env{
							File:   "./echo.env",
							Syntax: "sh:export",
						},
						Mount: &manifest.Mount{
							Path:     "/tmp/echo-mounts",
							ReadOnly: true,
						},
					},
					{
						Type:  manifest.TypeReplace,
						Name:  "echo-server/svc",
						Ports: []manifest.PortIdentifier{"all"},
					},
					{
						Type:      manifest.TypeIngest,
						Name:      "echo-sidecar",
						Container: "logger",
						Mount: &manifest.Mount{
							LocalMountPort: 1234,
						},
					},
					{
						Type:      manifest.TypeWiretap,
						Name:      "echo-tap",
						Workload:  "echo-server",
						Plaintext: true,
					},
				},
			},
		},
		{
			name: "attachments only, no connection",
			file: "testdata/valid-attachments-only.yaml",
			want: &manifest.State{
				APIVersion: "telepresence.io/v1alpha1",
				Kind:       "WorkstationState",
				Attachments: []manifest.Attachment{
					{
						Type: manifest.TypeIngest,
						Name: "echo-sidecar",
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := manifest.LoadFile(tt.file)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestLoadFile_Command(t *testing.T) {
	state, err := manifest.LoadFile("testdata/valid-command.yaml")
	require.NoError(t, err)
	require.Len(t, state.Attachments, 1)
	assert.Equal(t, []string{"node", "server.js"}, state.Attachments[0].Command)
}

func TestLoadFile_PortIdentifierFromIntegerAndString(t *testing.T) {
	state, err := manifest.LoadFile("testdata/valid-full.yaml")
	require.NoError(t, err)
	require.Len(t, state.Attachments, 4)
	ports := state.Attachments[0].Ports
	require.Len(t, ports, 2)
	assert.Equal(t, manifest.PortIdentifier("8080:http"), ports[0])
	assert.Equal(t, "8080:http", ports[0].String())
	assert.Equal(t, manifest.PortIdentifier("9090"), ports[1])
	assert.Equal(t, "9090", ports[1].String())
}

func TestMount_IsEnabled(t *testing.T) {
	tests := []struct {
		name  string
		mount *manifest.Mount
		want  bool
	}{
		{"default (nil enabled)", &manifest.Mount{}, true},
		{"explicit true", &manifest.Mount{Enabled: new(true)}, true},
		{"explicit false", &manifest.Mount{Enabled: new(false)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.mount.IsEnabled())
		})
	}
}

func TestLoadFile_Invalid(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		wantErr string
	}{
		{
			name:    "unknown property on attachment",
			file:    "testdata/invalid-unknown-property.yaml",
			wantErr: "/attachments/0",
		},
		{
			name:    "toPod on wiretap",
			file:    "testdata/invalid-topod-on-wiretap.yaml",
			wantErr: "/attachments/0",
		},
		{
			name:    "neither connection nor attachments",
			file:    "testdata/invalid-empty.yaml",
			wantErr: "missing property 'connection'",
		},
		{
			name:    "wrong kind",
			file:    "testdata/invalid-wrong-kind.yaml",
			wantErr: "/kind",
		},
		{
			name:    "duplicate attachment names",
			file:    "testdata/invalid-duplicate-name.yaml",
			wantErr: "echo-sidecar",
		},
		{
			name:    "bad env.syntax value",
			file:    "testdata/invalid-env-syntax.yaml",
			wantErr: "/attachments/0/env/syntax",
		},
		{
			name:    "replace name/container conflict",
			file:    "testdata/invalid-replace-container-conflict.yaml",
			wantErr: "echo-server/svc",
		},
		{
			name:    "empty command",
			file:    "testdata/invalid-empty-command.yaml",
			wantErr: "/attachments/0/command",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := manifest.LoadFile(tt.file)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestLoadFile_NotFound(t *testing.T) {
	_, err := manifest.LoadFile("testdata/does-not-exist.yaml")
	require.Error(t, err)
}
