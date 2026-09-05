package helm

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	core "k8s.io/api/core/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/charts"
)

func TestDefaultValues(t *testing.T) {
	data, err := charts.TelepresenceFS.ReadFile(charts.TelepresenceChartName + "/values.yaml")
	require.NoError(t, err)
	want, err := ParseValues(data)
	require.NoError(t, err)

	got, err := DefaultValues()
	require.NoError(t, err)
	assert.Equal(t, want, got)

	// The returned value must not be the memoized instance.
	got.Image = Image{Name: new("mutated")}
	again, err := DefaultValues()
	require.NoError(t, err)
	assert.Equal(t, want, again)
}

// TestValuesMapRoundTrip proves ToMap and ValuesFromMap agree: a value that
// has already passed through the map form once must produce the same map
// form again, including the empty, non-nil slices and maps values.yaml sets
// explicitly with `[]`/`{}` (omitzero, unlike omitempty, keeps those).
func TestValuesMapRoundTrip(t *testing.T) {
	dv, err := DefaultValues()
	require.NoError(t, err)

	m, err := dv.ToMap()
	require.NoError(t, err)

	back, err := ValuesFromMap(m)
	require.NoError(t, err)

	m2, err := back.ToMap()
	require.NoError(t, err)
	assert.Equal(t, m, m2)
}

func TestValuesFromMapUnknownKey(t *testing.T) {
	_, err := ValuesFromMap(map[string]any{
		"agent": map[string]any{
			"bogus": true,
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent.bogus")
}

func TestValuesFromMapUnknownTopLevelKey(t *testing.T) {
	_, err := ValuesFromMap(map[string]any{"notARealKey": 1})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "notARealKey")
}

// TestExplicitEmptyListSurvives proves that client.dns.excludeSuffixes: []
// (meaning "no exclusions") stays an explicit empty list, rather than being
// dropped so Helm falls back to the chart's default, through every path an
// operator's values can take: parsing, the map form, and re-marshaling.
func TestExplicitEmptyListSurvives(t *testing.T) {
	t.Run("ParseValues keeps a non-nil empty slice", func(t *testing.T) {
		v, err := ParseValues([]byte("client:\n  dns:\n    excludeSuffixes: []\n"))
		require.NoError(t, err)
		require.NotNil(t, v.Client.DNS.ExcludeSuffixes)
		assert.Empty(t, v.Client.DNS.ExcludeSuffixes)
	})

	t.Run("ToMap keeps the empty list", func(t *testing.T) {
		v, err := ParseValues([]byte("client:\n  dns:\n    excludeSuffixes: []\n"))
		require.NoError(t, err)

		m, err := v.ToMap()
		require.NoError(t, err)
		clientMap, ok := m["client"].(map[string]any)
		require.True(t, ok)
		dnsMap, ok := clientMap["dns"].(map[string]any)
		require.True(t, ok)
		excludeSuffixes, ok := dnsMap["excludeSuffixes"]
		require.True(t, ok, "excludeSuffixes must survive as an explicit key, not be dropped")
		assert.Equal(t, []any{}, excludeSuffixes)
	})

	t.Run("FromUnstructured keeps a non-nil empty slice", func(t *testing.T) {
		back, err := ValuesFromMap(map[string]any{
			"client": map[string]any{"dns": map[string]any{"excludeSuffixes": []any{}}},
		})
		require.NoError(t, err)
		require.NotNil(t, back.Client.DNS.ExcludeSuffixes)
		assert.Empty(t, back.Client.DNS.ExcludeSuffixes)
	})

	t.Run("yaml.Marshal prints the empty list rather than omitting it", func(t *testing.T) {
		v := &Values{Client: Client{DNS: ClientDNS{ExcludeSuffixes: []string{}}}}
		data, err := yaml.Marshal(v)
		require.NoError(t, err)
		assert.Contains(t, string(data), "excludeSuffixes: []")
	})

	t.Run("DiffValues treats an empty list as set", func(t *testing.T) {
		a := &Values{Client: Client{DNS: ClientDNS{ExcludeSuffixes: []string{}}}}
		assert.Equal(t, []string{"client.dns.excludeSuffixes"}, DiffValues(a, &Values{}))
	})
}

// TestEmptyBlockOmitted proves that a struct block with every field left at
// its zero value is indistinguishable from an absent one: it is dropped from
// ToMap and YAML output, and contributes nothing to a diff.
func TestEmptyBlockOmitted(t *testing.T) {
	v := &Values{Client: Client{}}

	m, err := v.ToMap()
	require.NoError(t, err)
	_, ok := m["client"]
	assert.False(t, ok, "an empty client block must not appear in the map form")

	data, err := yaml.Marshal(v)
	require.NoError(t, err)
	assert.Equal(t, "{}\n", string(data))

	assert.Empty(t, DiffValues(v, &Values{}))
}

// TestSchemaCoverage walks values.schema.yaml's properties (resolving $ref into
// $defs) and asserts that Values, or the nested struct at that path, has a field
// whose json tag matches. A chart key added to the schema without a struct field
// fails here instead of silently dropping out of the typed model.
func TestSchemaCoverage(t *testing.T) {
	data, err := charts.TelepresenceFS.ReadFile(charts.TelepresenceChartName + "/values.schema.yaml")
	require.NoError(t, err)

	var schema map[string]any
	require.NoError(t, yaml.Unmarshal(data, &schema))
	defs, _ := schema["$defs"].(map[string]any)

	var missing []string
	walkSchema("", schema, defs, reflect.TypeOf(Values{}), &missing)
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("struct fields missing for schema paths:\n%s", strings.Join(missing, "\n"))
	}
}

func walkSchema(path string, node map[string]any, defs map[string]any, t reflect.Type, missing *[]string) {
	node = resolveSchemaRef(node, defs)
	props, _ := node["properties"].(map[string]any)
	if props == nil {
		return
	}
	fields := jsonFields(t)
	for key, propAny := range props {
		fp := key
		if path != "" {
			fp = path + "." + key
		}
		ft, ok := fields[key]
		if !ok {
			*missing = append(*missing, fp)
			continue
		}
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() != reflect.Struct || ft.PkgPath() != pkgPath {
			continue
		}
		prop, _ := propAny.(map[string]any)
		walkSchema(fp, prop, defs, ft, missing)
	}
}

func resolveSchemaRef(node map[string]any, defs map[string]any) map[string]any {
	ref, ok := node["$ref"].(string)
	if !ok {
		return node
	}
	const prefix = "#/$defs/"
	if !strings.HasPrefix(ref, prefix) {
		return node
	}
	resolved, _ := defs[strings.TrimPrefix(ref, prefix)].(map[string]any)
	return resolved
}

func TestMergeValues(t *testing.T) {
	t.Run("scalar in over wins", func(t *testing.T) {
		over := &Values{LogLevel: new("debug")}
		base := &Values{LogLevel: new("info")}
		got := MergeValues(over, base)
		assert.Equal(t, "debug", *got.LogLevel)
	})

	t.Run("scalar unset in over adopts base", func(t *testing.T) {
		over := &Values{}
		base := &Values{LogLevel: new("info")}
		got := MergeValues(over, base)
		assert.Equal(t, "info", *got.LogLevel)
	})

	t.Run("nested struct set on both sides recurses", func(t *testing.T) {
		over := &Values{QuicTunnel: QuicTunnel{Enabled: new(true)}}
		base := &Values{QuicTunnel: QuicTunnel{Enabled: new(false), Port: new(int32(7778))}}
		got := MergeValues(over, base)
		assert.True(t, *got.QuicTunnel.Enabled)
		require.NotNil(t, got.QuicTunnel.Port)
		assert.Equal(t, int32(7778), *got.QuicTunnel.Port)
	})

	t.Run("nested struct set only in over is used as-is", func(t *testing.T) {
		base := &Values{}
		over := &Values{QuicTunnel: QuicTunnel{Enabled: new(true)}}
		got := MergeValues(over, base)
		assert.True(t, *got.QuicTunnel.Enabled)
	})

	t.Run("slice in over replaces base whole", func(t *testing.T) {
		over := &Values{Namespaces: []string{"a"}}
		base := &Values{Namespaces: []string{"b", "c"}}
		got := MergeValues(over, base)
		assert.Equal(t, []string{"a"}, got.Namespaces)
	})

	t.Run("unset slice in over adopts base", func(t *testing.T) {
		over := &Values{}
		base := &Values{Namespaces: []string{"b", "c"}}
		got := MergeValues(over, base)
		assert.Equal(t, []string{"b", "c"}, got.Namespaces)
	})

	t.Run("a k8s API struct recurses field by field, keeping both sides' settings", func(t *testing.T) {
		over := &Values{NamespaceSelector: meta.LabelSelector{MatchLabels: map[string]string{"team": "dev"}}}
		base := &Values{NamespaceSelector: meta.LabelSelector{
			MatchExpressions: []meta.LabelSelectorRequirement{{Key: "env", Operator: meta.LabelSelectorOpExists}},
		}}
		got := MergeValues(over, base)
		assert.Equal(t, map[string]string{"team": "dev"}, got.NamespaceSelector.MatchLabels)
		require.Len(t, got.NamespaceSelector.MatchExpressions, 1)
		assert.Equal(t, "env", got.NamespaceSelector.MatchExpressions[0].Key)
	})

	t.Run("inputs are not mutated", func(t *testing.T) {
		over := &Values{Namespaces: []string{"a"}, QuicTunnel: QuicTunnel{Enabled: new(true)}}
		base := &Values{Namespaces: []string{"b"}, QuicTunnel: QuicTunnel{Port: new(int32(1))}}
		got := MergeValues(over, base)
		got.Namespaces[0] = "z"
		got.QuicTunnel.Port = new(int32(99))
		assert.Equal(t, "a", over.Namespaces[0])
		assert.Equal(t, "b", base.Namespaces[0])
		assert.Equal(t, int32(1), *base.QuicTunnel.Port)
	})
}

func TestMergeValues_KeepsOmittedProbeFields(t *testing.T) {
	base := &Values{LivenessProbe: core.Probe{TimeoutSeconds: 17, PeriodSeconds: 23, FailureThreshold: 9}}
	over := &Values{LogLevel: new("debug")}
	got := MergeValues(over, base)
	assert.Equal(t, core.Probe{TimeoutSeconds: 17, PeriodSeconds: 23, FailureThreshold: 9}, got.LivenessProbe)

	over = &Values{LivenessProbe: core.Probe{PeriodSeconds: 5}}
	got = MergeValues(over, base)
	assert.Equal(t, core.Probe{TimeoutSeconds: 17, PeriodSeconds: 5, FailureThreshold: 9}, got.LivenessProbe)
}

func TestDiffValues(t *testing.T) {
	t.Run("equal values yield no diff", func(t *testing.T) {
		a := &Values{LogLevel: new("info")}
		b := &Values{LogLevel: new("info")}
		assert.Empty(t, DiffValues(a, b))
	})

	t.Run("differing scalar reported by dotted path", func(t *testing.T) {
		a := &Values{QuicTunnel: QuicTunnel{Enabled: new(true)}}
		b := &Values{QuicTunnel: QuicTunnel{Enabled: new(false)}}
		assert.Equal(t, []string{"quicTunnel.enabled"}, DiffValues(a, b))
	})

	t.Run("nested struct set only in a reported by its leaves", func(t *testing.T) {
		a := &Values{QuicTunnel: QuicTunnel{Enabled: new(true)}}
		b := &Values{}
		assert.Equal(t, []string{"quicTunnel.enabled"}, DiffValues(a, b))
	})

	t.Run("slice treated as a leaf", func(t *testing.T) {
		a := &Values{Namespaces: []string{"a", "b"}}
		b := &Values{Namespaces: []string{"a"}}
		assert.Equal(t, []string{"namespaces"}, DiffValues(a, b))
	})

	t.Run("results are sorted", func(t *testing.T) {
		a := &Values{LogLevel: new("debug"), HostNetwork: new(true)}
		b := &Values{}
		assert.Equal(t, []string{"hostNetwork", "logLevel"}, DiffValues(a, b))
	})
}
