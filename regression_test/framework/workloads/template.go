// Package workloads renders test workload manifests (Deployment/StatefulSet +
// Service) from embedded templates.
package workloads

import (
	"bytes"
	"embed"
	"fmt"
	"text/template"
)

//go:embed templates/*.yaml
var templatesFS embed.FS

const (
	// echoImage is the test echo-server image; it always listens on echoPort.
	echoImage = "ghcr.io/telepresenceio/echo-server:0.3.1"
	echoPort  = 8080
)

// Template describes a workload manifest to render.
type Template struct {
	Name     string
	Kind     string // "Deployment" or "StatefulSet"
	Replicas int
	Image    string
	Port     int32
	SvcName  string
}

// Echo returns a single-replica Deployment+Service template running the
// echo-server test image on one HTTP port.
func Echo(name string) Template {
	return Template{
		Name:     name,
		Kind:     "Deployment",
		Replicas: 1,
		Image:    echoImage,
		Port:     echoPort,
		SvcName:  name,
	}
}

// EchoStatefulSet is Echo, rendered as a StatefulSet instead of a Deployment.
func EchoStatefulSet(name string) Template {
	t := Echo(name)
	t.Kind = "StatefulSet"
	return t
}

// Render executes the embedded template matching t.Kind in namespace and
// returns the resulting manifest YAML.
func (t Template) Render(namespace string) (string, error) {
	var file string
	switch t.Kind {
	case "Deployment":
		file = "templates/deployment.yaml"
	case "StatefulSet":
		file = "templates/statefulset.yaml"
	default:
		return "", fmt.Errorf("workloads: unknown kind %q", t.Kind)
	}
	tpl, err := template.ParseFS(templatesFS, file)
	if err != nil {
		return "", err
	}
	data := struct {
		Template
		Namespace string
	}{t, namespace}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}
