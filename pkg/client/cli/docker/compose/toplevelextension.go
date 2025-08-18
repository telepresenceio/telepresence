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

func ParseTopLevelExtension(v any) (*topLevelExtension, error) {
	data, err := client.MarshalJSON(v)
	if err != nil {
		return nil, err
	}
	var tle topLevelExtension
	err = client.UnmarshalJSON(data, &tle, true)
	if err != nil {
		return nil, err
	}
	if count := len(tle.Connections); count > 1 {
		// Assert that all connections have a name and that the names are unique.
		unique := make(map[string]struct{}, count)
		for _, cc := range tle.Connections {
			if cc.Name == "" {
				return nil, fmt.Errorf("connection name is required when multiple connections are defined")
			}
			if _, ok := unique[cc.Name]; ok {
				return nil, fmt.Errorf("duplicate connection name %q", cc.Name)
			}
			unique[cc.Name] = struct{}{}
		}
	}
	for _, m := range tle.Mounts {
		if m.VolumePattern != nil {
			if m.Volume != "" {
				return nil, fmt.Errorf("volumePattern and volume are mutually exclusive")
			}
		}
	}
	return &tle, nil
}
