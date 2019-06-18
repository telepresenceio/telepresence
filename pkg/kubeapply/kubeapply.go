package kubeapply

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/datawire/teleproxy/pkg/k8s"
	"github.com/pkg/errors"
)

// Kubeapply applies the supplied manifests to the kubernetes cluster
// indicated via the kubeinfo argument. If kubeinfo is nil, it will
// look in the standard default places for cluster configuration.
func Kubeapply(kubeinfo *k8s.KubeInfo, timeout time.Duration, debug bool, files ...string) error {
	if kubeinfo == nil {
		kubeinfo = k8s.NewKubeInfo("", "", "")
	}
	p := newPhaser()

	for _, file := range files {
		err := p.Add(file)
		if err != nil {
			return err
		}
	}

	for _, names := range p.phases() {
		err := phase(kubeinfo, timeout, debug, names, nil)
		if err != nil {
			return err
		}
	}

	return nil
}

type phaser struct {
	prefixes map[string][]string
	last     []string
}

func newPhaser() *phaser {
	return &phaser{
		prefixes: make(map[string][]string),
	}
}

func isYaml(name string) bool {
	for _, ext := range []string{
		".yaml",
	} {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}

func (p *phaser) Add(root string) error {
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() && isYaml(path) {
			p.AddFile(path)
		}

		return nil
	})
	return err
}

func (p *phaser) AddFile(path string) {
	base := filepath.Base(path)
	var pfx string
	if len(base) >= 3 {
		pfx = base[:3]
	}
	if len(pfx) == 3 && pfx[2] == '-' && unicode.IsDigit(rune(pfx[0])) && unicode.IsDigit(rune(pfx[1])) {
		p.prefixes[pfx] = append(p.prefixes[pfx], path)
	} else {
		p.last = append(p.last, path)
	}
}

func (p *phaser) phases() (result [][]string) {
	var keys []string
	for k := range p.prefixes {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		result = append(result, p.prefixes[k])
	}

	if len(p.last) > 0 {
		result = append(result, p.last)
	}
	return
}

func phase(kubeinfo *k8s.KubeInfo, timeout time.Duration, debug bool, names []string, data interface{}) error {
	expanded, err := expand(names, data)
	if err != nil {
		return err
	}

	cli, err := k8s.NewClient(kubeinfo)
	if err != nil {
		return errors.Wrapf(err, "kubeapply: error connecting to cluster %s", kubeinfo.Kubeconfig)
	}
	waiter, err := k8s.NewWaiter(cli.Watcher())
	if err != nil {
		return err
	}

	valid := make(map[string]bool)
	var msgs []string
	for _, n := range expanded {
		err := waiter.Scan(n)
		if err != nil {
			msgs = append(msgs, fmt.Sprintf("%s: %v\n", n, err))
			valid[n] = false
		} else {
			valid[n] = true
		}
	}

	if len(msgs) == 0 {
		err = apply(kubeinfo, expanded)
	}

	if !debug {
		for _, n := range expanded {
			if valid[n] {
				err := os.Remove(n)
				if err != nil {
					log.Print(err)
				}
			}
		}
	}

	if err != nil {
		return err
	}

	if len(msgs) > 0 {
		return errors.Errorf("errors expanding templates:\n  %s", strings.Join(msgs, "\n  "))
	}

	if !waiter.Wait(timeout) {
		return errors.Errorf("not ready after %s seconds\n", timeout.String())
	}

	return nil
}

func expand(names []string, data interface{}) ([]string, error) {
	_ = data
	fmt.Printf("expanding %s\n", strings.Join(names, " "))
	var result []string
	for _, n := range names {
		resources, err := k8s.LoadResources(n)
		if err != nil {
			return nil, err
		}
		out := n + ".o"
		err = k8s.SaveResources(out, resources)
		if err != nil {
			return nil, err
		}
		result = append(result, out)
	}
	return result, nil
}

func apply(info *k8s.KubeInfo, names []string) error {
	args := []string{"apply"}
	for _, n := range names {
		args = append(args, "-f", n)
	}
	kargs, err := info.GetKubectlArray(args...)
	if err != nil {
		return err
	}
	fmt.Printf("kubectl %s\n", strings.Join(kargs, " "))
	/* #nosec */
	cmd := exec.Command("kubectl", kargs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if err != nil {
		return err
	}

	return nil
}
