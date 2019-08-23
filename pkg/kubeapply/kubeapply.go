package kubeapply

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/pkg/errors"

	"github.com/datawire/teleproxy/pkg/k8s"
)

// errorDeadlineExceeded is returned from YAMLCollection.applyAndWait
// if the deadline is exceeded.
var errorDeadlineExceeded = errors.New("timeout exceeded")

// Kubeapply applies the supplied manifests to the kubernetes cluster
// indicated via the kubeinfo argument.  If kubeinfo is nil, it will
// look in the standard default places for cluster configuration.  If
// any phase takes longer than perPhaseTimeout to become ready, then
// it returns early with an error.
func Kubeapply(kubeinfo *k8s.KubeInfo, perPhaseTimeout time.Duration, debug, dryRun bool, files ...string) error {
	collection, err := CollectYAML(files...)
	if err != nil {
		return err
	}

	if err = collection.ApplyAndWait(kubeinfo, perPhaseTimeout, debug, dryRun); err != nil {
		return err
	}

	return nil
}

// A YAMLCollection is a collection of YAML files to later be applied.
type YAMLCollection map[string][]string

// CollectYAML takes several file or directory paths, and returns a
// collection of the YAML files in them.
func CollectYAML(paths ...string) (YAMLCollection, error) {
	ret := make(YAMLCollection)
	for _, path := range paths {
		err := filepath.Walk(path, func(filename string, fileinfo os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if fileinfo.IsDir() {
				return nil
			}

			if strings.HasSuffix(filename, ".yaml") {
				ret.addFile(filename)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return ret, nil
}

func hasNumberPrefix(filepart string) bool {
	if len(filepart) < 3 {
		return false
	}
	return '0' <= filepart[0] && filepart[0] <= '9' &&
		'0' <= filepart[1] && filepart[1] <= '9' &&
		filepart[2] == '-'
}

func (collection YAMLCollection) addFile(path string) {
	_, notdir := filepath.Split(path)
	phaseName := "last" // all letters sort after all numbers; "last" is after all numbered phases
	if hasNumberPrefix(notdir) {
		phaseName = notdir[:2]
	}

	collection[phaseName] = append(collection[phaseName], path)
}

// ApplyAndWait applies the collection of YAML, and waits for all
// Resources described in it to be ready.  If any phase takes longer
// than perPhaseTimeout to become ready, then it returns early with an
// error.
func (collection YAMLCollection) ApplyAndWait(
	kubeinfo *k8s.KubeInfo,
	perPhaseTimeout time.Duration,
	debug, dryRun bool,
) error {
	if kubeinfo == nil {
		kubeinfo = k8s.NewKubeInfo("", "", "")
	}

	phaseNames := make([]string, 0, len(collection))
	for phaseName := range collection {
		phaseNames = append(phaseNames, phaseName)
	}
	sort.Strings(phaseNames)

	for _, phaseName := range phaseNames {
		deadline := time.Now().Add(perPhaseTimeout)
		err := applyAndWait(kubeinfo, deadline, debug, dryRun, collection[phaseName])
		if err != nil {
			if err == errorDeadlineExceeded {
				err = errors.Errorf("phase %q not ready after %v", phaseName, perPhaseTimeout)
			}
			return err
		}
	}
	return nil
}

func applyAndWait(kubeinfo *k8s.KubeInfo, deadline time.Time, debug, dryRun bool, filenames []string) error {
	expanded, err := expand(filenames)
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
		err = kubectlApply(kubeinfo, dryRun, expanded)
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

	if !waiter.Wait(deadline) {
		return errorDeadlineExceeded
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

func kubectlApply(info *k8s.KubeInfo, dryRun bool, filenames []string) error {
	args := []string{"apply"}
	if dryRun {
		args = append(args, "--dry-run")
	}
	for _, filename := range filenames {
		// https://github.com/datawire/teleproxy/issues/77
		filehandle, err := os.Open(filename)
		if err != nil {
			return err
		}
		defer filehandle.Close()
		if err := syscall.Flock(int(filehandle.Fd()), syscall.LOCK_EX); err != nil {
			return err
		}
		args = append(args, "-f", filename)
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
