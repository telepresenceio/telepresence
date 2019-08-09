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

	"github.com/datawire/teleproxy/pkg/k8s"
	"github.com/pkg/errors"
)

// Kubeapply applies the supplied manifests to the kubernetes cluster
// indicated via the kubeinfo argument. If kubeinfo is nil, it will
// look in the standard default places for cluster configuration.
func Kubeapply(kubeinfo *k8s.KubeInfo, timeout time.Duration, debug, dryRun bool, files ...string) error {
	if kubeinfo == nil {
		kubeinfo = k8s.NewKubeInfo("", "", "")
	}
	p := &phaser{
		phasesByName: make(map[string][]string),
	}

	for _, file := range files {
		err := p.Add(file)
		if err != nil {
			return err
		}
	}

	for _, names := range p.orderedPhases() {
		err := phase(kubeinfo, timeout, debug, dryRun, names)
		if err != nil {
			return err
		}
	}

	return nil
}

type phaser struct {
	phasesByName map[string][]string
}

func isYaml(name string) bool {
	return strings.HasSuffix(name, ".yaml")
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

func hasNumberPrefix(filepart string) bool {
	if len(filepart) < 3 {
		return false
	}
	return '0' <= filepart[0] && filepart[0] <= '9' &&
		'0' <= filepart[1] && filepart[1] <= '9' &&
		filepart[2] == '-'
}

func (p *phaser) AddFile(path string) {
	_, notdir := filepath.Split(path)
	phaseName := "last" // all letters sort after all numbers; "last" is after all numbered phases
	if hasNumberPrefix(notdir) {
		phaseName = notdir[:2]
	}
	p.phasesByName[phaseName] = append(p.phasesByName[phaseName], path)
}

func (p *phaser) orderedPhases() [][]string {
	phaseNames := make([]string, 0, len(p.phasesByName))
	for phaseName := range p.phasesByName {
		phaseNames = append(phaseNames, phaseName)
	}
	sort.Strings(phaseNames)

	orderedPhases := make([][]string, 0, len(phaseNames))
	for _, phaseName := range phaseNames {
		orderedPhases = append(orderedPhases, p.phasesByName[phaseName])
	}

	return orderedPhases
}

func phase(kubeinfo *k8s.KubeInfo, timeout time.Duration, debug, dryRun bool, names []string) error {
	expanded, err := expand(names)
	if err != nil {
		return err
	}

	cli, err := k8s.NewClient(kubeinfo)
	if err != nil {
		return errors.Wrapf(err, "kubeapply: error connecting to cluster %v", kubeinfo)
	}
	waiter, err := NewWaiter(cli.Watcher())
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
		err = apply(kubeinfo, dryRun, expanded)
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

func expand(names []string) ([]string, error) {
	fmt.Printf("expanding %s\n", strings.Join(names, " "))
	var result []string
	for _, n := range names {
		resources, err := LoadResources(n)
		if err != nil {
			return nil, err
		}
		out := n + ".o"
		err = SaveResources(out, resources)
		if err != nil {
			return nil, err
		}
		result = append(result, out)
	}
	return result, nil
}

func apply(info *k8s.KubeInfo, dryRun bool, names []string) error {
	args := []string{"apply"}
	if dryRun {
		args = append(args, "--dry-run")
	}
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
