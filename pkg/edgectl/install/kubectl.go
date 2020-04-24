package edgectl

import (
	"bytes"
	"encoding/json"
	"io"
	"os/exec"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sVersion "k8s.io/apimachinery/pkg/version"

	"github.com/datawire/ambassador/internal/pkg/edgectl"
)

const (
	// defKubectlExe is the default `kubectl` executable
	defKubectlExe = "kubectl"
)

// getKubectlPath returns the full path to the kubectl executable, or an error if not found
func getKubectlPath() (string, error) {
	return exec.LookPath(defKubectlExe)
}

type KubernetesVersion struct {
	Client k8sVersion.Info `json:"clientVersion"`
	Server k8sVersion.Info `json:"serverVersion"`
}

type Runner interface {
	Run(args ...string) error
}

// Kubectl is a simple interface for an abstract kubectl
type Kubectl interface {
	Runner

	Create(what, name, namespace string) error
	Apply(content, namespace string) error
	Get(what, name, namespace string) (*unstructured.Unstructured, error)
	List(what, namespace string, labels []string) (*unstructured.Unstructured, error)
	Exec(pod, cont, namespace string, args ...string) (string, error)
	Describe(what, namespace string) (string, error)
	Logs(what, namespace string, args ...string) (string, error)
	Version() (KubernetesVersion, error)
	ClusterInfo() (string, error)

	WithStdin(io.Reader) Kubectl
	WithStdout(io.Writer) Kubectl
	WithStderr(io.Writer) Kubectl
}

////////////////////////////////////////////////////////////////////////////////

// SimpleRunner
type SimpleRunner struct {
	name   string
	exe    string
	args   []string
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

// SimpleKubectl is a simple `kubectl` wrapper
type SimpleKubectl struct {
	name   string
	exe    string
	args   []string
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer

	// output is used just for testing: fill it and it will be used as the result of running kubectl
	// TODO: we should use a proper interface and so on...
	output string
}

func (k SimpleKubectl) Run(args ...string) error {
	kargs := k.args
	kargs = append(kargs, args...)

	cmd := exec.Command(k.exe, kargs...)
	cmd.Stdin = k.stdin
	cmd.Stdout = k.stdout
	cmd.Stderr = k.stderr
	if err := cmd.Run(); err != nil {
		return errors.Wrap(err, k.name)
	}
	return nil
}

func (k SimpleKubectl) RunAsBytes(args ...string) ([]byte, error) {
	buf := bytes.NewBufferString(k.output)
	if buf.Len() == 0 {
		if err := k.WithStdout(io.MultiWriter(k.stdout, buf)).Run(args...); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

func (k SimpleKubectl) RunAsUnstructured(args ...string) (*unstructured.Unstructured, error) {
	b, err := k.RunAsBytes(args...)
	if err != nil {
		return nil, err
	}

	res := map[string]interface{}{}
	if err := json.Unmarshal(b, &res); err != nil {
		return nil, err
	}
	return &unstructured.Unstructured{Object: res}, nil
}

func (k SimpleKubectl) Create(what, name, namespace string) error {
	args := []string{"create", what, name}
	if len(namespace) > 0 {
		args = append(args, "-n", namespace)
	}
	return k.Run(args...)
}

func (k SimpleKubectl) Apply(content, namespace string) error {
	args := []string{"apply", "-f", "-"}
	if len(namespace) > 0 {
		args = append(args, "-n", namespace)
	}
	return k.WithStdin(bytes.NewBufferString(content)).Run(args...)
}

func (k SimpleKubectl) Get(what, name, namespace string) (*unstructured.Unstructured, error) {
	args := []string{"get", what, name, "-o", "json"}
	if len(namespace) > 0 {
		args = append(args, "-n", namespace)
	}
	return k.RunAsUnstructured(args...)
}

func (k SimpleKubectl) List(what, namespace string, labels []string) (*unstructured.Unstructured, error) {
	args := []string{"get", what, "-o", "json"}
	if len(labels) > 0 {
		for _, l := range labels {
			args = append(args, "-l", l)
		}
	}
	if len(namespace) > 0 {
		args = append(args, "-n", namespace)
	}
	return k.RunAsUnstructured(args...)
}

func (k SimpleKubectl) Exec(pod, cont, namespace string, args ...string) (string, error) {
	largs := []string{"exec", pod, "-c", cont}
	if len(namespace) > 0 {
		largs = append(largs, "-n", namespace)
	}
	largs = append(largs, args...)

	s, err := k.RunAsBytes(largs...)
	return string(s), err
}

func (k SimpleKubectl) Describe(what, namespace string) (string, error) {
	largs := []string{"describe", what}
	if len(namespace) > 0 {
		largs = append(largs, "-n", namespace)
	}
	s, err := k.RunAsBytes(largs...)
	return string(s), err
}

func (k SimpleKubectl) Logs(what, namespace string, args ...string) (string, error) {
	largs := []string{"logs", what}
	if len(namespace) > 0 {
		largs = append(largs, "-n", namespace)
	}
	largs = append(largs, args...)

	s, err := k.RunAsBytes(largs...)
	return string(s), err
}

func (k SimpleKubectl) Version() (KubernetesVersion, error) {
	b, err := k.RunAsBytes("version", "-o", "json")
	if err != nil {
		return KubernetesVersion{}, err
	}
	kv := KubernetesVersion{}
	if err := json.Unmarshal(b, &kv); err != nil {
		return KubernetesVersion{}, err
	}
	return kv, nil
}

func (k SimpleKubectl) ClusterInfo() (string, error) {
	buf := &bytes.Buffer{}
	err := k.WithStdout(io.MultiWriter(k.stdout, buf)).Run("cluster-info")
	if err != nil {
		return "", err
	}
	return buf.String(), nil
}

func (k SimpleKubectl) WithStdin(reader io.Reader) Kubectl {
	k.stdin = reader
	return k
}

func (k SimpleKubectl) WithStdout(writer io.Writer) Kubectl {
	k.stdout = writer
	return k
}

func (k SimpleKubectl) WithStderr(writer io.Writer) Kubectl {
	k.stderr = writer
	return k
}

// NewSimpleKubectl creates a new, simple kubectl runner
func (i *Installer) NewSimpleKubectl() (res SimpleKubectl, err error) {
	kubectl, err := getKubectlPath()
	if err != nil {
		err = errors.Wrapf(err, "kubectl not found")
		return
	}

	// get the list of common arguments (like `--kubeconfig`)
	args, err := i.kubeinfo.GetKubectlArray()
	if err != nil {
		err = errors.Wrapf(err, "cluster access")
		return
	}

	res = SimpleKubectl{
		exe:    kubectl,
		args:   args,
		stdin:  bytes.NewBufferString(""),
		stdout: edgectl.NewLoggingWriter(i.cmdOut),
		stderr: edgectl.NewLoggingWriter(i.cmdErr),
	}
	return
}
