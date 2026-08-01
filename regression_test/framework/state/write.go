package state

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// apiVersion/kind are fixed by state.schema.yaml's const constraints; State
// doesn't expose them.
const (
	apiVersion = "telepresence.io/v1alpha1"
	kind       = "WorkstationState"
)

// document is the on-disk shape sigs.k8s.io/yaml marshals, with json tags
// matching pkg/client/cli/manifest/types.go exactly; the exported
// State/Connection/Attachment/... types above are what suites build.
type document struct {
	APIVersion  string          `json:"apiVersion"`
	Kind        string          `json:"kind"`
	Connection  *connectionDoc  `json:"connection,omitempty"`
	Attachments []attachmentDoc `json:"attachments,omitempty"`
}

type connectionDoc struct {
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
	Vnat                    []string          `json:"vnat,omitempty"`
	ProxyVia                []proxyViaDoc     `json:"proxyVia,omitempty"`
	RerouteLocal            []string          `json:"rerouteLocal,omitempty"`
	RerouteRemote           []string          `json:"rerouteRemote,omitempty"`
}

type proxyViaDoc struct {
	Subnet   string `json:"subnet"`
	Workload string `json:"workload"`
}

type attachmentDoc struct {
	Type      AttachmentType `json:"type"`
	Name      string         `json:"name"`
	Namespace string         `json:"namespace,omitempty"`
	Container string         `json:"container,omitempty"`
	Env       *envDoc        `json:"env,omitempty"`
	Mount     *mountDoc      `json:"mount,omitempty"`
	NodeAgent *bool          `json:"nodeAgent,omitempty"`
	Command   []string       `json:"command,omitempty"`

	Workload  string   `json:"workload,omitempty"`
	Service   string   `json:"service,omitempty"`
	Ports     []string `json:"ports,omitempty"`
	Address   string   `json:"address,omitempty"`
	Mechanism string   `json:"mechanism,omitempty"`

	HTTPHeaders        []string `json:"httpHeaders,omitempty"`
	HTTPPathEqualities []string `json:"httpPathEqualities,omitempty"`
	HTTPPathPrefixes   []string `json:"httpPathPrefixes,omitempty"`
	HTTPPathRegexps    []string `json:"httpPathRegexps,omitempty"`
	Plaintext          bool     `json:"plaintext,omitempty"`

	Metadata map[string]string `json:"metadata,omitempty"`
	ToPod    []string          `json:"toPod,omitempty"`
}

type envDoc struct {
	File   string `json:"file,omitempty"`
	Syntax string `json:"syntax,omitempty"`
	JSON   string `json:"json,omitempty"`
}

type mountDoc struct {
	Enabled        *bool  `json:"enabled,omitempty"`
	Path           string `json:"path,omitempty"`
	ReadOnly       bool   `json:"readOnly,omitempty"`
	LocalMountPort uint16 `json:"localMountPort,omitempty"`
}

// Render returns s's manifest YAML encoding, with apiVersion/kind injected.
func (s *State) Render() ([]byte, error) {
	doc := document{APIVersion: apiVersion, Kind: kind}
	if s.Connection != nil {
		c := s.Connection
		var proxyVia []proxyViaDoc
		for _, pv := range c.ProxyVia {
			proxyVia = append(proxyVia, proxyViaDoc(pv))
		}
		doc.Connection = &connectionDoc{
			Name:                    c.Name,
			Kubeconfig:              c.Kubeconfig,
			Context:                 c.Context,
			Namespace:               c.Namespace,
			KubeFlags:               c.KubeFlags,
			ManagerNamespace:        c.ManagerNamespace,
			MappedNamespaces:        c.MappedNamespaces,
			AlsoProxy:               c.AlsoProxy,
			NeverProxy:              c.NeverProxy,
			AllowConflictingSubnets: c.AllowConflictingSubnets,
			Vnat:                    c.Vnat,
			ProxyVia:                proxyVia,
			RerouteLocal:            c.RerouteLocal,
			RerouteRemote:           c.RerouteRemote,
		}
	}
	for _, a := range s.Attachments {
		ad := attachmentDoc{
			Type:               a.Type,
			Name:               a.Name,
			Namespace:          a.Namespace,
			Container:          a.Container,
			NodeAgent:          a.NodeAgent,
			Command:            a.Command,
			Workload:           a.Workload,
			Service:            a.Service,
			Ports:              a.Ports,
			Address:            a.Address,
			Mechanism:          a.Mechanism,
			HTTPHeaders:        a.HTTPHeaders,
			HTTPPathEqualities: a.HTTPPathEqualities,
			HTTPPathPrefixes:   a.HTTPPathPrefixes,
			HTTPPathRegexps:    a.HTTPPathRegexps,
			Plaintext:          a.Plaintext,
			Metadata:           a.Metadata,
			ToPod:              a.ToPod,
		}
		if a.Env != nil {
			ad.Env = &envDoc{File: a.Env.File, Syntax: a.Env.Syntax, JSON: a.Env.JSON}
		}
		if a.Mount != nil {
			ad.Mount = &mountDoc{
				Enabled:        a.Mount.Enabled,
				Path:           a.Mount.Path,
				ReadOnly:       a.Mount.ReadOnly,
				LocalMountPort: a.Mount.LocalMountPort,
			}
		}
		doc.Attachments = append(doc.Attachments, ad)
	}
	return yaml.Marshal(doc)
}

// Write renders s and writes it under ArtifactDir("state") as state.yaml,
// returning its path for `telepresence apply/delete -f <path>`. The
// containing directory is content-addressed so two suites requesting an
// identical manifest share one file, mirroring rt.KubeConfigCopy's idiom
// (rt/kubeconfig.go).
func (s *State) Write(e rt.Env) (path string, err error) {
	e.T.Helper()
	data, err := s.Render()
	if err != nil {
		return "", fmt.Errorf("state: rendering manifest: %w", err)
	}
	dir := e.R.ArtifactDir("state", sha256Hex(data)[:16])
	path = filepath.Join(dir, "state.yaml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("state: writing %s: %w", path, err)
	}
	return path, nil
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
