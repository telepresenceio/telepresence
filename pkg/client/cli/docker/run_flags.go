package docker

import (
	"fmt"
	"strings"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/flags"
)

type Volume struct {
	Name    string
	Target  string
	Options string
}

func (v *Volume) String() string {
	n := v.Name
	if n == "" {
		n = v.Target
	} else {
		n += ":" + v.Target
	}
	if v.Options != "" {
		n += ":" + v.Options
	}
	return n
}

type Mount struct {
	Type    string
	Source  string
	Target  string
	Options string
}

type Network struct {
	Name    string
	Aliases []string
}

func (m *Mount) String() string {
	sb := new(strings.Builder)
	sb.WriteString("type=")
	sb.WriteString(m.Type)
	if m.Source != "" {
		sb.WriteString(",src=")
		sb.WriteString(m.Source)
	}
	if m.Target != "" {
		sb.WriteString(",dst=")
		sb.WriteString(m.Target)
	}
	if m.Options != "" {
		sb.WriteByte(',')
		sb.WriteString(m.Options)
	}
	return sb.String()
}

type RunFlags struct {
	Volumes []Volume
	Mounts  []Mount
}

func ParseRunFlags(args []string) (*RunFlags, []string, error) {
	f := RunFlags{}
	values, err := flags.GetUnparsedValues("volume", 'v', args)
	if err != nil {
		return nil, nil, err
	}
	for _, av := range values {
		vx := strings.Split(av, ":")
		v := Volume{}
		switch len(vx) {
		case 1:
			v.Target = vx[0]
		case 2:
			v.Name = vx[0]
			v.Target = vx[1]
		case 3:
			v.Name = vx[0]
			v.Target = vx[1]
			v.Options = vx[2]
		default:
			return nil, nil, fmt.Errorf("invalid volume format: %s", av)
		}
		f.Volumes = append(f.Volumes, v)
	}
	values, err = flags.GetUnparsedValues("mount", 0, args)
	if err != nil {
		return nil, nil, err
	}
	for _, av := range values {
		m := Mount{}
		for _, vx := range strings.Split(av, ",") {
			kv := strings.Split(vx, "=")
			if len(kv) != 2 {
				return nil, nil, fmt.Errorf("invalid mount format: %s", av)
			}
			key := kv[0]
			val := kv[1]
			switch key {
			case "type":
				m.Type = val
			case "src", "source":
				m.Source = val
			case "destination", "dst", "target":
				m.Target = val
			default:
				if len(m.Options) > 0 {
					m.Options += ","
				}
				m.Options += vx
			}
		}
		f.Mounts = append(f.Mounts, m)
	}
	return &f, args, nil
}
