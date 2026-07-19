package manifest

import (
	"encoding/json"
	"fmt"
	"strconv"
)

type State struct {
	APIVersion  string       `json:"apiVersion"`
	Kind        string       `json:"kind"`
	Connection  *Connection  `json:"connection,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

type Connection struct {
	Name                    string            `json:"name,omitempty"`
	Kubeconfig              string            `json:"kubeconfig,omitempty"`
	Context                 string            `json:"context,omitempty"`
	Namespace               string            `json:"namespace,omitempty"`
	KubeFlags               map[string]string `json:"kubeFlags,omitempty"`
	ManagerNamespace        string            `json:"managerNamespace,omitempty"`
	MappedNamespaces        []string          `json:"mappedNamespaces,omitempty"`
	AlsoProxy               []string          `json:"alsoProxy,omitempty"`
	NeverProxy              []string          `json:"neverProxy,omitempty"`
	AllowConflictingSubnets []string          `json:"allowConflictingSubnets,omitempty"`
	ProxyVia                []ProxyVia        `json:"proxyVia,omitempty"`
	RerouteLocal            []string          `json:"rerouteLocal,omitempty"`
	RerouteRemote           []string          `json:"rerouteRemote,omitempty"`
}

type ProxyVia struct {
	Subnet   string `json:"subnet"`
	Workload string `json:"workload"`
}

type AttachmentType string

const (
	TypeIntercept AttachmentType = "intercept"
	TypeReplace   AttachmentType = "replace"
	TypeIngest    AttachmentType = "ingest"
	TypeWiretap   AttachmentType = "wiretap"
)

type Attachment struct {
	Type      AttachmentType `json:"type"`
	Name      string         `json:"name"`
	Namespace string         `json:"namespace,omitempty"`
	Container string         `json:"container,omitempty"`
	Env       *Env           `json:"env,omitempty"`
	Mount     *Mount         `json:"mount,omitempty"`
	NodeAgent *bool          `json:"nodeAgent,omitempty"`
	Command   []string       `json:"command,omitempty"`

	Workload  string           `json:"workload,omitempty"`
	Service   string           `json:"service,omitempty"`
	Ports     []PortIdentifier `json:"ports,omitempty"`
	Address   string           `json:"address,omitempty"`
	Mechanism string           `json:"mechanism,omitempty"`

	HTTPHeaders        []string `json:"httpHeaders,omitempty"`
	HTTPPathEqualities []string `json:"httpPathEqualities,omitempty"`
	HTTPPathPrefixes   []string `json:"httpPathPrefixes,omitempty"`
	HTTPPathRegexps    []string `json:"httpPathRegexps,omitempty"`
	Plaintext          bool     `json:"plaintext,omitempty"`

	Metadata map[string]string `json:"metadata,omitempty"`
	ToPod    []string          `json:"toPod,omitempty"`
}

type Env struct {
	File   string `json:"file,omitempty"`
	Syntax string `json:"syntax,omitempty"`
	JSON   string `json:"json,omitempty"`
}

type Mount struct {
	Enabled        *bool  `json:"enabled,omitempty"`
	Path           string `json:"path,omitempty"`
	ReadOnly       bool   `json:"readOnly,omitempty"`
	LocalMountPort uint16 `json:"localMountPort,omitempty"`
}

func (m *Mount) IsEnabled() bool {
	return m.Enabled == nil || *m.Enabled
}

type PortIdentifier string

func (p *PortIdentifier) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*p = PortIdentifier(s)
		return nil
	}
	n, err := strconv.Atoi(string(data))
	if err != nil {
		return fmt.Errorf("invalid port identifier %q: %w", data, err)
	}
	*p = PortIdentifier(strconv.Itoa(n))
	return nil
}

func (p PortIdentifier) String() string {
	return string(p)
}
