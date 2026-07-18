package manifest

import (
	"bytes"
	_ "embed"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

//go:embed state.schema.yaml
var schemaYAML []byte

const schemaID = "https://telepresence.io/schemas/workstation-state.v1alpha1.yaml"

var compileSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	schemaJSON, err := yaml.YAMLToJSON(schemaYAML)
	if err != nil {
		return nil, fmt.Errorf("converting embedded manifest schema to JSON: %w", err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
	if err != nil {
		return nil, fmt.Errorf("parsing embedded manifest schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(schemaID, doc); err != nil {
		return nil, fmt.Errorf("adding embedded manifest schema: %w", err)
	}
	return c.Compile(schemaID)
})

// Load reads a manifest from r, validates it against the embedded schema,
// and decodes it into a State.
func Load(r io.Reader) (*State, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, errcat.User.Errorf(err, "reading manifest")
	}
	return decode(raw)
}

// LoadFile reads a manifest from the given path. A path of "-" reads from
// stdin.
func LoadFile(path string) (*State, error) {
	var r io.Reader
	if path == "-" {
		r = os.Stdin
	} else {
		f, err := os.Open(path)
		if err != nil {
			return nil, errcat.User.Errorf(err, "opening manifest %q", path)
		}
		defer f.Close()
		r = f
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, errcat.User.Errorf(err, "reading manifest %q", path)
	}
	return decode(raw)
}

func decode(raw []byte) (*State, error) {
	jsonBytes, err := yaml.YAMLToJSON(raw)
	if err != nil {
		return nil, errcat.User.Errorf(err, "parsing manifest")
	}
	schema, err := compileSchema()
	if err != nil {
		return nil, fmt.Errorf("compiling embedded manifest schema: %w", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(jsonBytes))
	if err != nil {
		return nil, errcat.User.Errorf(err, "parsing manifest")
	}
	if err := schema.Validate(inst); err != nil {
		return nil, errcat.User.Errorf(err, "manifest validation failed")
	}
	state := &State{}
	if err := yaml.UnmarshalStrict(raw, state); err != nil {
		return nil, errcat.User.Errorf(err, "decoding manifest")
	}
	if err := validateSemantics(state); err != nil {
		return nil, err
	}
	return state, nil
}

func validateSemantics(state *State) error {
	seen := make(map[string]bool, len(state.Attachments))
	for _, a := range state.Attachments {
		if seen[a.Name] {
			return errcat.User.Newf("duplicate attachment name %q", a.Name)
		}
		seen[a.Name] = true
		if a.Type == TypeReplace && a.Container != "" && strings.Contains(a.Name, "/") {
			return errcat.User.Newf(
				"attachment %q: name already designates a container; the container property cannot also be used", a.Name)
		}
	}
	return nil
}
