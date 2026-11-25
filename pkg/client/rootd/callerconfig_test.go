package rootd

import (
	"encoding/base64"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/json"
)

const testPEM = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"

// sectionTypes returns the type of each top-level section of the client configuration, keyed by JSON name.
func sectionTypes() map[string]reflect.Type {
	ct := reflect.TypeOf(client.GetDefaultConfig().Base()).Elem()
	sections := make(map[string]reflect.Type)
	for _, f := range reflect.VisibleFields(ct) {
		if !f.IsExported() || f.Anonymous {
			continue
		}
		name := f.Name
		if tag, _, _ := strings.Cut(f.Tag.Get("json"), ","); tag != "" {
			name = tag
		}
		sections[name] = f.Type
	}
	return sections
}

func TestCallerSections_decisionForEverySection(t *testing.T) {
	sections := sectionTypes()
	require.NotEmpty(t, sections)
	for name := range sections {
		_, ok := callerSections[name]
		assert.True(t, ok, "the client configuration section %q has no decision in callerSections", name)
	}
	for name := range callerSections {
		_, ok := sections[name]
		// The network section exists on Windows only.
		assert.True(t, ok || name == "network", "callerSections has a decision for %q, which is not a section of the client configuration", name)
	}
}

func TestCallerSections_takenSectionsCanBeCopied(t *testing.T) {
	cfg := client.GetDefaultConfig()
	for name, decision := range callerSections {
		assert.Equal(t, decision != ownSection, copySection(name, cfg, cfg), "section %q", name)
	}
}

// carriesText reports whether a value of type t can hold text.
func carriesText(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.String:
		return true
	case reflect.Slice, reflect.Array, reflect.Pointer:
		return carriesText(t.Elem())
	case reflect.Map:
		return carriesText(t.Key()) || carriesText(t.Elem())
	case reflect.Struct:
		for _, f := range reflect.VisibleFields(t) {
			if f.IsExported() && !f.Anonymous && carriesText(f.Type) {
				return true
			}
		}
	default:
	}
	return false
}

func TestCallerStringFields_decisionForEveryTextField(t *testing.T) {
	found := make(map[string]bool)
	for name, st := range sectionTypes() {
		if callerSections[name] == ownSection {
			continue
		}
		for _, f := range reflect.VisibleFields(st) {
			if !f.IsExported() || f.Anonymous || !carriesText(f.Type) {
				continue
			}
			jn := f.Name
			if tag, _, _ := strings.Cut(f.Tag.Get("json"), ","); tag != "" {
				jn = tag
			}
			key := name + "." + jn
			found[key] = true
			_, ok := callerStringFields[key]
			assert.True(t, ok, "the field %s is taken from the caller and carries text, but has no entry in callerStringFields", key)
		}
	}
	for key := range callerStringFields {
		assert.True(t, found[key], "callerStringFields has an entry for %s, which is not a text field of a section taken from the caller", key)
	}
}

func callerConfigJSON(t *testing.T, mutate func(cfg client.Config)) []byte {
	t.Helper()
	cfg := client.GetDefaultConfig()
	mutate(cfg)
	data, err := json.Marshal(cfg)
	require.NoError(t, err)
	return data
}

func TestSessionConfig_takesAllowedSectionsFromTheCaller(t *testing.T) {
	data := callerConfigJSON(t, func(cfg client.Config) {
		cfg.DNS().Excludes = []string{"example.com"}
		cfg.DNS().LookupTimeout = 7 * time.Second
		cfg.Routing().AlsoProxy = []netip.Prefix{netip.MustParsePrefix("10.1.0.0/16")}
		cfg.Cluster().DefaultManagerNamespace = "tm"
		cfg.Cluster().ForceSPDY = true
		cfg.Timeouts().PrivateHelm = 3 * time.Minute
		cfg.Grpc().WatchRetryInterval = 3 * time.Second
	})
	cfg, err := sessionConfig(t.Context(), client.GetDefaultConfig(), data, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"example.com"}, cfg.DNS().Excludes)
	assert.Equal(t, 7*time.Second, cfg.DNS().LookupTimeout)
	assert.Equal(t, []netip.Prefix{netip.MustParsePrefix("10.1.0.0/16")}, cfg.Routing().AlsoProxy)
	assert.Equal(t, "tm", cfg.Cluster().DefaultManagerNamespace)
	assert.True(t, cfg.Cluster().ForceSPDY)
	assert.Equal(t, 3*time.Minute, cfg.Timeouts().Get(client.TimeoutHelm))
	assert.Equal(t, 3*time.Second, cfg.Grpc().WatchRetryInterval)
}

func TestSessionConfig_keepsTheOwnSections(t *testing.T) {
	own := client.GetDefaultConfig()
	own.Images().PrivateRegistry = "own.example.com"
	own.Intercept().MountsRoot = "/own/mounts"
	own.Intercept().SshfsPath = "/own/sshfs"
	own.Docker().HostGateway = "own-gateway"
	own.Helm().ChartURL = "oci://own.example.com/chart"
	own.NodeAgent().Enabled = false
	own.Usage().Enabled = false

	data := callerConfigJSON(t, func(cfg client.Config) {
		cfg.Images().PrivateRegistry = "evil.example.com"
		cfg.Images().PrivateAgentImage = "evil/agent:1"
		cfg.Intercept().MountsRoot = "/etc"
		cfg.Intercept().SshfsPath = "/tmp/evil"
		cfg.Docker().HostGateway = "evil-gateway"
		cfg.Helm().ChartURL = "oci://evil.example.com/chart"
		cfg.NodeAgent().Enabled = true
		cfg.Usage().Enabled = true
		cfg.Cluster().ForceSPDY = true
	})
	cfg, err := sessionConfig(t.Context(), own, data, false)
	require.NoError(t, err)
	assert.Equal(t, "own.example.com", cfg.Images().PrivateRegistry)
	assert.Empty(t, cfg.Images().PrivateAgentImage)
	assert.Equal(t, "/own/mounts", cfg.Intercept().MountsRoot)
	assert.Equal(t, "/own/sshfs", cfg.Intercept().SshfsPath)
	assert.Equal(t, "own-gateway", cfg.Docker().HostGateway)
	assert.Equal(t, "oci://own.example.com/chart", cfg.Helm().ChartURL)
	assert.False(t, cfg.NodeAgent().Enabled)
	assert.False(t, cfg.Usage().Enabled)
	assert.True(t, cfg.Cluster().ForceSPDY)
}

func TestSessionConfig_doesNotChangeTheOwnConfiguration(t *testing.T) {
	own := client.GetDefaultConfig()
	data := callerConfigJSON(t, func(cfg client.Config) {
		cfg.DNS().Excludes = []string{"example.com"}
	})
	_, err := sessionConfig(t.Context(), own, data, false)
	require.NoError(t, err)
	assert.Empty(t, own.DNS().Excludes)
}

func TestSessionConfig_logLevels(t *testing.T) {
	own := client.GetDefaultConfig()
	data := callerConfigJSON(t, func(cfg client.Config) {
		cfg.LogLevels().RootDaemon = -4
	})
	cfg, err := sessionConfig(t.Context(), own, data, false)
	require.NoError(t, err)
	assert.EqualValues(t, -4, cfg.LogLevels().RootDaemon)

	cfg, err = sessionConfig(t.Context(), own, data, true)
	require.NoError(t, err)
	assert.Equal(t, own.LogLevels().RootDaemon, cfg.LogLevels().RootDaemon)
}

func TestSessionConfig_emptyCallerConfiguration(t *testing.T) {
	own := client.GetDefaultConfig()
	own.Cluster().ForceSPDY = true
	cfg, err := sessionConfig(t.Context(), own, nil, false)
	require.NoError(t, err)
	assert.True(t, cfg.Cluster().ForceSPDY)
}

func TestSessionConfig_managerServerCA(t *testing.T) {
	tests := []struct {
		name    string
		spec    string
		want    string
		wantErr bool
	}{
		{name: "unset"},
		{name: "pem", spec: testPEM, want: testPEM},
		{name: "text before the pem", spec: "subject: x\n" + testPEM, want: testPEM},
		{name: "base64", spec: base64.StdEncoding.EncodeToString([]byte(testPEM)), want: testPEM},
		{name: "path", spec: "/etc/shadow", wantErr: true},
		{name: "relative path", spec: "ca/ca.pem", wantErr: true},
		{name: "base64 of something else", spec: base64.StdEncoding.EncodeToString([]byte("hello")), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := callerConfigJSON(t, func(cfg client.Config) {
				cfg.Cluster().ManagerServerCA = tt.spec
			})
			cfg, err := sessionConfig(t.Context(), client.GetDefaultConfig(), data, false)
			if tt.wantErr {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), tt.spec)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.Cluster().ManagerServerCA)
			if tt.want != "" {
				assert.True(t, strings.HasPrefix(cfg.Cluster().ManagerServerCA, pemPrefix))
			}
		})
	}
}

func TestSessionConfig_dropsInvalidSuffixes(t *testing.T) {
	data := callerConfigJSON(t, func(cfg client.Config) {
		cfg.DNS().IncludeSuffixes = []string{".good", "../../evil", "a/b"}
		cfg.DNS().ExcludeSuffixes = []string{".ok", "x\ny"}
	})
	cfg, err := sessionConfig(t.Context(), client.GetDefaultConfig(), data, false)
	require.NoError(t, err)
	assert.Equal(t, []string{".good"}, cfg.DNS().IncludeSuffixes)
	assert.Equal(t, []string{".ok"}, cfg.DNS().ExcludeSuffixes)
}
