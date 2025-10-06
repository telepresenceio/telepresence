package itest

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	core "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/pkg/dos"
)

type TplResource interface {
	Apply(context context.Context, namespace string) error
	Delete(ctx context.Context) error
}

type ContainerPort struct {
	Number   int
	Name     string
	Protocol core.Protocol
}

type ServicePort struct {
	Number     int
	Name       string
	Protocol   core.Protocol
	TargetPort string
}

type tplBase struct {
	yml []byte
	ns  string
}

func (b *tplBase) loadAndApply(ctx context.Context, path, ns string, data any) error {
	yml, err := ReadTemplate(ctx, filepath.Join("testdata", "k8s", path+".goyaml"), data)
	if err == nil {
		b.yml = yml
		b.ns = ns
		err = Kubectl(dos.WithStdin(ctx, bytes.NewReader(b.yml)), b.ns, "apply", "-f", "-")
	}
	return err
}

func (b *tplBase) Delete(ctx context.Context) error {
	return Kubectl(dos.WithStdin(ctx, bytes.NewReader(b.yml)), b.ns, "delete", "-f", "-")
}

type Generic struct {
	tplBase
	Name           string
	Annotations    map[string]string
	Labels         map[string]string
	Environment    []core.EnvVar
	TargetPort     string
	ServicePorts   []ServicePort
	ContainerPort  int
	ContainerPorts []ContainerPort
	Image          string
	Registry       string
	ServiceAccount string
	Volumes        []core.Volume
	VolumeMounts   []core.VolumeMount
}

func (g *Generic) Apply(ctx context.Context, ns string) error {
	return g.loadAndApply(ctx, "generic", ns, g)
}

type PersistentVolume struct {
	tplBase
	Name             string
	Annotations      map[string]string
	StorageClassName string
}

func (p *PersistentVolume) Apply(ctx context.Context, ns string) error {
	return p.loadAndApply(ctx, "pv", ns, p)
}

type PersistentVolumeClaim struct {
	tplBase
	Name             string
	Annotations      map[string]string
	StorageClassName string
}

func (r *PersistentVolumeClaim) Apply(ctx context.Context, ns string) error {
	return r.loadAndApply(ctx, "pvc", ns, r)
}

type DisruptionBudget struct {
	Name           string
	MinAvailable   int
	MaxUnavailable int
}

func OpenTemplate(ctx context.Context, path string, data any) (io.Reader, error) {
	b, err := ReadTemplate(ctx, path, data)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(b), nil
}

func ReadTemplate(ctx context.Context, path string, data any) ([]byte, error) {
	fnMap := sprig.FuncMap()
	fnMap["toYaml"] = toYAML
	tpl, err := template.New("").Funcs(fnMap).ParseFiles(path)
	if err != nil {
		return nil, err
	}
	wr := bytes.Buffer{}
	if err = tpl.ExecuteTemplate(&wr, filepath.Base(path), data); err != nil {
		return nil, err
	}
	return wr.Bytes(), nil
}

func EvalTemplate(content string, data any) ([]byte, error) {
	fnMap := sprig.FuncMap()
	fnMap["toYaml"] = toYAML
	tpl, err := template.New("embedded").Funcs(fnMap).Parse(content)
	if err != nil {
		return nil, err
	}
	wr := bytes.Buffer{}
	if err = tpl.ExecuteTemplate(&wr, "embedded", data); err != nil {
		return nil, err
	}
	return wr.Bytes(), nil
}

// toYAML is direct copy of toYaml in the helm.sh/helm/v3/pkg/engine package.
func toYAML(v interface{}) string {
	data, err := yaml.Marshal(v)
	if err != nil {
		// Swallow errors inside of a template.
		return ""
	}
	return strings.TrimSuffix(string(data), "\n")
}
