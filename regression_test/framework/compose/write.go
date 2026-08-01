package compose

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// document is the on-disk shape sigs.k8s.io/yaml marshals; the exported
// Project/Connection/MountPolicy/Service/Volume types are what suites build,
// this is only the wire format.
type document struct {
	XTele    *xTeleTop             `json:"x-tele,omitempty"`
	Services map[string]serviceDoc `json:"services,omitempty"`
	Volumes  map[string]volumeDoc  `json:"volumes,omitempty"`
}

type xTeleTop struct {
	Connections []connectionDoc  `json:"connections,omitempty"`
	Mounts      []mountPolicyDoc `json:"mounts,omitempty"`
}

type connectionDoc struct {
	Name             string   `json:"name,omitempty"`
	Namespace        string   `json:"namespace,omitempty"`
	AlsoProxy        []string `json:"also-proxy,omitempty"`
	NeverProxy       []string `json:"never-proxy,omitempty"`
	ManagerNamespace string   `json:"manager-namespace,omitempty"`
	MappedNamespaces []string `json:"mapped-namespaces,omitempty"`
}

type mountPolicyDoc struct {
	Volume        string `json:"volume,omitempty"`
	VolumePattern string `json:"volumePattern,omitempty"`
	Policy        string `json:"policy,omitempty"`
}

type serviceDoc struct {
	XTele       map[string]any    `json:"x-tele,omitempty"`
	Image       string            `json:"image,omitempty"`
	Command     []string          `json:"command,omitempty"`
	Ports       []string          `json:"ports,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	Volumes     []string          `json:"volumes,omitempty"`
}

type volumeDoc struct {
	Name string `json:"name,omitempty"`
}

// Render returns p's docker-compose.yml encoding.
func (p *Project) Render() ([]byte, error) {
	var doc document
	if len(p.Connections) > 0 || len(p.Mounts) > 0 {
		top := &xTeleTop{}
		for _, c := range p.Connections {
			top.Connections = append(top.Connections, connectionDoc(c))
		}
		for _, mp := range p.Mounts {
			top.Mounts = append(top.Mounts, mountPolicyDoc(mp))
		}
		doc.XTele = top
	}
	if len(p.Services) > 0 {
		doc.Services = make(map[string]serviceDoc, len(p.Services))
		for name, s := range p.Services {
			sd := serviceDoc{
				Image:       s.Image,
				Command:     s.Command,
				Ports:       s.Ports,
				Environment: s.Environment,
				Volumes:     s.Volumes,
			}
			if s.XTele != nil {
				sd.XTele = s.XTele.xTele()
			}
			doc.Services[name] = sd
		}
	}
	if len(p.Volumes) > 0 {
		doc.Volumes = make(map[string]volumeDoc, len(p.Volumes))
		for name, v := range p.Volumes {
			doc.Volumes[name] = volumeDoc{Name: v.Name}
		}
	}
	return yaml.Marshal(doc)
}

// Write renders p and writes it under ArtifactDir("compose") as
// docker-compose.yml, returning its path for `telepresence compose -f
// <path> ...`. The containing directory is content-addressed so two suites
// requesting an identical project share one file, mirroring
// rt.KubeConfigCopy's idiom (rt/kubeconfig.go).
func (p *Project) Write(e rt.Env) (path string, err error) {
	e.T.Helper()
	data, err := p.Render()
	if err != nil {
		return "", fmt.Errorf("compose: rendering project: %w", err)
	}
	dir := e.R.ArtifactDir("compose", sha256Hex(data)[:16])
	path = filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("compose: writing %s: %w", path, err)
	}
	return path, nil
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
