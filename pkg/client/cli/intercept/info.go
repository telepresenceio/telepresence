package intercept

import (
	"strings"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
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
	ServicePortID string            `json:"service_port_id,omitempty" yaml:"service_port_id,omitempty"`
	Environment   map[string]string `json:"environment,omitempty"     yaml:"environment,omitempty"`
	MountPoint    string            `json:"mount_point,omitempty"     yaml:"mount_point,omitempty"`
	MountError    string            `json:"mount_error,omitempty"     yaml:"mount_error,omitempty"`
	HttpFilter    []string          `json:"http_filter,omitempty"     yaml:"http_filter,omitempty"`
	Global        bool              `json:"global,omitempty"          yaml:"global,omitempty"`
	PreviewURL    string            `json:"preview_url,omitempty"     yaml:"preview_url,omitempty"`
	Ingress       *Ingress          `json:"ingress,omitempty"         yaml:"ingress,omitempty"`
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

func NewInfo(ii *manager.InterceptInfo, mountError string) *Info {
	spec := ii.Spec
	return &Info{
		ID:            ii.Id,
		Name:          spec.Name,
		Disposition:   ii.Disposition.String(),
		Message:       ii.Message,
		WorkloadKind:  spec.WorkloadKind,
		TargetHost:    spec.TargetHost,
		TargetPort:    spec.TargetPort,
		ServicePortID: spec.ServicePortName,
		Environment:   ii.Environment,
		MountPoint:    ii.MountPoint,
		MountError:    mountError,
		HttpFilter:    spec.MechanismArgs,
		Global:        spec.Mechanism == "tcp",
		PreviewURL:    PreviewURL(ii.PreviewDomain),
		Ingress:       NewIngress(ii.PreviewSpec),
	}
}
