package compose

import (
	"fmt"
	"regexp"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

type volumeMountPolicy struct {
	Volume        string            `json:"volume,omitempty"`
	VolumePattern *regexp.Regexp    `json:"volumePattern,omitempty"`
	Policy        types.MountPolicy `json:"policy"`
}

func (v *volumeMountPolicy) Matches(volumeName string) bool {
	return v.VolumePattern != nil && v.VolumePattern.MatchString(volumeName) || v.Volume == volumeName
}

type topLevelExtension struct {
	Connections []*connectionConfig `json:"connections,omitempty"`
	Mounts      []volumeMountPolicy `json:"mounts,omitempty"`
}

func (tl *topLevelExtension) parse(v any) error {
	data, err := client.MarshalJSON(v)
	if err != nil {
		return err
	}
	err = client.UnmarshalJSON(data, tl, true)
	if err != nil {
		return err
	}
	if count := len(tl.Connections); count > 1 {
		// Assert that all connections have a name and that the names are unique.
		unique := make(map[string]struct{}, count)
		for _, cc := range tl.Connections {
			if cc.Name == "" {
				return fmt.Errorf("connection name is required when multiple connections are defined")
			}
			if _, ok := unique[cc.Name]; ok {
				return fmt.Errorf("duplicate connection name %q", cc.Name)
			}
			unique[cc.Name] = struct{}{}
		}
	}
	for _, m := range tl.Mounts {
		if m.VolumePattern != nil {
			if m.Volume != "" {
				return fmt.Errorf("volumePattern and volume are mutually exclusive")
			}
		}
	}
	return nil
}
