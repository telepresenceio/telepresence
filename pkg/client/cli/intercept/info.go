package intercept

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/mount"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

type Ingress struct {
	Host   string `json:"host,omitempty"    yaml:"host,omitempty"`
	Port   int32  `json:"port,omitempty"    yaml:"port,omitempty"`
	UseTLS bool   `json:"use_tls,omitempty" yaml:"use_tls,omitempty"`
	L5Host string `json:"l5host,omitempty"  yaml:"l5host,omitempty"`
}

type Info struct {
	ID            string            `json:"id,omitempty"              yaml:"id,omitempty"`
	Name          string            `json:"name,omitempty"            yaml:"name,omitempty"`
	Disposition   string            `json:"disposition,omitempty"     yaml:"disposition,omitempty"`
	Message       string            `json:"message,omitempty"         yaml:"message,omitempty"`
	WorkloadKind  string            `json:"workload_kind,omitempty"   yaml:"workload_kind,omitempty"`
	TargetHost    string            `json:"target_host,omitempty"     yaml:"target_host,omitempty"`
	TargetPort    int32             `json:"target_port,omitempty"     yaml:"target_port,omitempty"`
	PodPorts      []string          `json:"pod_ports,omitempty"       yaml:"pod_ports,omitempty"`
	ServiceUID    string            `json:"service_uid,omitempty"     yaml:"service_uid,omitempty"`
	ServicePortID string            `json:"service_port_id,omitempty" yaml:"service_port_id,omitempty"` // ServicePortID is deprecated. Use PortID
	PortID        string            `json:"port_id,omitempty"         yaml:"port_id,omitempty"`
	ContainerPort int32             `json:"container_port,omitempty"  yaml:"container_port,omitempty"`
	Protocol      string            `json:"protocol,omitempty"        yaml:"protocol,omitempty"`
	Environment   map[string]string `json:"environment,omitempty"     yaml:"environment,omitempty"`
	Mount         *mount.Info       `json:"mount,omitempty"           yaml:"mount,omitempty"`
	FilterDesc    string            `json:"filter_desc,omitempty"     yaml:"filter_desc,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"        yaml:"metadata,omitempty"`
	HttpFilter    []string          `json:"http_filter,omitempty"     yaml:"http_filter,omitempty"`
	Global        bool              `json:"global,omitempty"          yaml:"global,omitempty"`
	PreviewURL    string            `json:"preview_url,omitempty"     yaml:"preview_url,omitempty"`
	Ingress       *Ingress          `json:"ingress,omitempty"         yaml:"ingress,omitempty"`
	PodIP         string            `json:"pod_ip,omitempty"          yaml:"pod_ip,omitempty"`
	debug         bool
}

func NewIngress(ps *manager.PreviewSpec) *Ingress {
	if ps == nil {
		return nil
	}
	ii := ps.Ingress
	if ii == nil {
		return nil
	}
	return &Ingress{
		Host:   ii.Host,
		Port:   ii.Port,
		UseTLS: ii.UseTls,
		L5Host: ii.L5Host,
	}
}

func PreviewURL(pu string) string {
	if !(pu == "" || strings.HasPrefix(pu, "https://") || strings.HasPrefix(pu, "http://")) {
		pu = "https://" + pu
	}
	return pu
}

func NewInfo(ctx context.Context, ii *manager.InterceptInfo, ro bool, mountError error) *Info {
	spec := ii.Spec
	var m *mount.Info
	if mountError != nil {
		m = &mount.Info{Error: mountError.Error()}
	} else if ii.MountPoint != "" {
		m = mount.NewInfo(ctx, ii.Environment, ii.FtpPort, ii.SftpPort, ii.ClientMountPoint, ii.MountPoint, ii.PodIp, ro)
	}
	info := &Info{
		ID:            ii.Id,
		Name:          spec.Name,
		Disposition:   ii.Disposition.String(),
		Message:       ii.Message,
		WorkloadKind:  spec.WorkloadKind,
		TargetHost:    spec.TargetHost,
		TargetPort:    spec.TargetPort,
		PodPorts:      spec.PodPorts,
		Mount:         m,
		ServiceUID:    spec.ServiceUid,
		PortID:        spec.PortIdentifier,
		ContainerPort: spec.ContainerPort,
		Protocol:      spec.Protocol,
		PodIP:         ii.PodIp,
		Environment:   ii.Environment,
		FilterDesc:    ii.MechanismArgsDesc,
		Metadata:      ii.Metadata,
		HttpFilter:    spec.MechanismArgs,
		Global:        spec.Mechanism == "tcp",
		PreviewURL:    PreviewURL(ii.PreviewDomain),
		Ingress:       NewIngress(ii.PreviewSpec),
	}
	if spec.ServiceUid != "" {
		// For backward compatibility in JSON output
		info.ServicePortID = info.PortID
	}
	return info
}

func (ii *Info) WriteTo(w io.Writer) (int64, error) {
	kvf := ioutil.DefaultKeyValueFormatter()
	kvf.Prefix = "   "
	kvf.Add("Intercept name", ii.Name)
	kvf.Add("State", func() string {
		msg := ""
		if manager.InterceptDispositionType_value[ii.Disposition] > int32(manager.InterceptDispositionType_WAITING) {
			msg += "error: "
		}
		msg += ii.Disposition
		if ii.Message != "" {
			msg += ": " + ii.Message
		}
		return msg
	}())
	kvf.Add("Workload kind", ii.WorkloadKind)

	if ii.debug {
		kvf.Add("ID", ii.ID)
	}

	// Show all ports as mappings from containter port to local port.
	pkv := ioutil.DefaultKeyValueFormatter()
	pkv.Indent = ""
	pkv.Separator = " -> "
	if ii.PortID != "" {
		pm, _ := agentconfig.NewPortIdentifier(ii.Protocol, strconv.Itoa(int(ii.ContainerPort)))
		pkv.Add(pm.String(), fmt.Sprintf("%d %s", ii.TargetPort, ii.Protocol))
	}
	for _, pp := range ii.PodPorts {
		pm := agentconfig.PortMapping(pp)
		to := pm.To()
		pkv.Add(pm.From().String(), fmt.Sprintf("%d %s", to.Port, to.Proto))
	}
	kvf.Add("Intercepting", fmt.Sprintf("%s -> %s\n%s", ii.PodIP, ii.TargetHost, pkv))

	if !ii.Global {
		kvf.Add("Intercepting", func() string {
			if ii.FilterDesc != "" {
				return ii.FilterDesc
			}
			if ii.Global {
				return `using mechanism "tcp"`
			}
			return fmt.Sprintf("using mechanism=%q with args=%q", "http", ii.HttpFilter)
		}())

		if ii.PreviewURL != "" {
			previewURL := ii.PreviewURL
			// Right now SystemA gives back domains with the leading "https://", but
			// let's not rely on that.
			if !strings.HasPrefix(previewURL, "https://") && !strings.HasPrefix(previewURL, "http://") {
				previewURL = "https://" + previewURL
			}
			kvf.Add("Preview URL", previewURL)
		}
		if in := ii.Ingress; in != nil {
			kvf.Add("Layer 5 Hostname", in.L5Host)
		}
	}

	if m := ii.Mount; m != nil {
		if m.LocalDir != "" {
			kvf.Add("Volume Mount Point", m.LocalDir)
		} else if m.Error != "" {
			kvf.Add("Volume Mount Error", m.Error)
		}
	}

	if len(ii.Metadata) > 0 {
		kvf.Add("Metadata", fmt.Sprintf("%q", ii.Metadata))
	}
	return kvf.WriteTo(w)
}
