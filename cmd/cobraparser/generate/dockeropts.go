package generate

import (
	"fmt"

	units "github.com/docker/go-units"
)

// MemBytes and ListOpts are minimal pflag.Value implementations replacing
// github.com/docker/docker/opts.MemBytes and opts.ListOpts. The upstream opts
// package moved into the daemon-only github.com/moby/moby/v2 module when the
// Docker SDK split at v29, so they are reproduced here to avoid depending on a
// beta module for two small flag helpers. Behavior matches the originals.

// MemBytes is a pflag.Value for human-readable memory sizes (e.g. 128M, 2g).
type MemBytes int64

func (m *MemBytes) String() string {
	// pflag treats "0" as the zero value and hides it; "0 B" would be shown,
	// so return a bare "0" when empty.
	if v := int64(*m); v != 0 {
		return units.BytesSize(float64(v))
	}
	return "0"
}

func (m *MemBytes) Set(value string) error {
	val, err := units.RAMInBytes(value)
	*m = MemBytes(val)
	return err
}

func (m *MemBytes) Type() string { return "bytes" }

// ValidatorFctType validates and optionally transforms a value before it is
// appended to a ListOpts.
type ValidatorFctType func(val string) (string, error)

// ListOpts is a pflag.Value that accumulates repeated string values.
type ListOpts struct {
	values    *[]string
	validator ValidatorFctType
}

// NewListOptsRef creates a ListOpts backed by the given slice and validator.
func NewListOptsRef(values *[]string, validator ValidatorFctType) *ListOpts {
	return &ListOpts{values: values, validator: validator}
}

func (o *ListOpts) String() string {
	if len(*o.values) == 0 {
		return ""
	}
	return fmt.Sprintf("%v", *o.values)
}

func (o *ListOpts) Set(value string) error {
	if o.validator != nil {
		v, err := o.validator(value)
		if err != nil {
			return err
		}
		value = v
	}
	*o.values = append(*o.values, value)
	return nil
}

func (o *ListOpts) Type() string { return "list" }
