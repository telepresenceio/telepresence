package helm

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

// TestClientMirrorsPkgClientConfig walks pkg/client's config struct by
// reflection, recursing into struct fields, slice element structs, and the
// embedded OSSpecificConfig, and asserts Client has a field with a matching
// json tag at every path, reporting every path that is missing.
func TestClientMirrorsPkgClientConfig(t *testing.T) {
	cfgType := reflect.TypeOf(client.GetDefaultConfig().Base()).Elem()

	var missing []string
	walkClientConfig("", cfgType, reflect.TypeOf(Client{}), &missing)
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("helm.Client is missing fields for pkg/client config paths:\n%s", strings.Join(missing, "\n"))
	}
}

// walkClientConfig recurses through srcType, a pkg/client config struct, and
// checks that dstType declares a field with the same json tag at every leaf.
// A field anonymously embedded in srcType is walked inline, at the same
// path, since JSON promotes its own fields there too.
func walkClientConfig(path string, srcType, dstType reflect.Type, missing *[]string) {
	srcPkgPath := srcType.PkgPath()
	dstFields := jsonFields(dstType)
	for i := range srcType.NumField() {
		f := srcType.Field(i)
		if f.PkgPath != "" {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if f.Anonymous && name == "" {
			if ft := f.Type; ft.Kind() == reflect.Struct && ft.PkgPath() == srcPkgPath {
				walkClientConfig(path, ft, dstType, missing)
			}
			continue
		}
		if name == "" {
			name = f.Name
		}
		fp := name
		if path != "" {
			fp = path + "." + name
		}
		dt, ok := dstFields[name]
		if !ok {
			*missing = append(*missing, fp)
			continue
		}
		for dt.Kind() == reflect.Pointer {
			dt = dt.Elem()
		}
		walkClientConfigField(fp, f.Type, dt, srcPkgPath, missing)
	}
}

// walkClientConfigField recurses into a struct-typed or slice-of-struct-typed
// field, provided the struct belongs to pkg/client itself; every other shape
// (scalars, and slices or structs from other packages) is a leaf, already
// satisfied by the name lookup in walkClientConfig.
func walkClientConfigField(fp string, ft, dt reflect.Type, srcPkgPath string, missing *[]string) {
	switch {
	case ft.Kind() == reflect.Struct && ft.PkgPath() == srcPkgPath:
		if dt.Kind() != reflect.Struct {
			*missing = append(*missing, fp)
			return
		}
		walkClientConfig(fp, ft, dt, missing)
	case ft.Kind() == reflect.Slice:
		et := ft.Elem()
		for et.Kind() == reflect.Pointer {
			et = et.Elem()
		}
		if et.Kind() != reflect.Struct || et.PkgPath() != srcPkgPath {
			return
		}
		det := dt
		if det.Kind() == reflect.Slice {
			det = det.Elem()
			for det.Kind() == reflect.Pointer {
				det = det.Elem()
			}
		}
		if det.Kind() != reflect.Struct {
			*missing = append(*missing, fp)
			return
		}
		walkClientConfig(fp, et, det, missing)
	}
}
