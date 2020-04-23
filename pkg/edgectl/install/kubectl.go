package edgectl

import (
	"os/exec"
	"strings"

	"github.com/pkg/errors"

	"github.com/datawire/ambassador/internal/pkg/edgectl"
)

// getKubectlPath returns the full path to the kubectl executable, or an error if not found
func (i *Installer) getKubectlPath() (string, error) {
	return exec.LookPath("kubectl")
}

// showKubectl calls kubectl and dumps the output to the logger. Use this for
// side effects.
func (i *Installer) showKubectl(name string, input string, args ...string) error {
	kargs, err := i.kubeinfo.GetKubectlArray(args...)
	if err != nil {
		return errors.Wrapf(err, "cluster access for %s", name)
	}
	kubectl, err := i.getKubectlPath()
	if err != nil {
		return errors.Wrapf(err, "kubectl not found %s", name)
	}
	i.log.Printf("$ %v %s", kubectl, strings.Join(kargs, " "))
	cmd := exec.Command(kubectl, kargs...)
	cmd.Stdin = strings.NewReader(input)
	cmd.Stdout = edgectl.NewLoggingWriter(i.cmdOut)
	cmd.Stderr = edgectl.NewLoggingWriter(i.cmdErr)
	if err := cmd.Run(); err != nil {
		return errors.Wrap(err, name)
	}
	return nil
}

// captureKubectl calls kubectl and returns its stdout, dumping all the output
// to the logger.
func (i *Installer) captureKubectl(name, input string, args ...string) (res string, err error) {
	res = ""
	kargs, err := i.kubeinfo.GetKubectlArray(args...)
	if err != nil {
		err = errors.Wrapf(err, "cluster access for %s", name)
		return
	}
	kubectl, err := i.getKubectlPath()
	if err != nil {
		err = errors.Wrapf(err, "kubectl not found %s", name)
		return
	}
	kargs = append([]string{kubectl}, kargs...)
	return i.Capture(name, true, input, kargs...)
}

// silentCaptureKubectl calls kubectl and returns its stdout
// without dumping all the output to the logger.
func (i *Installer) silentCaptureKubectl(name, input string, args ...string) (res string, err error) {
	res = ""
	kargs, err := i.kubeinfo.GetKubectlArray(args...)
	if err != nil {
		err = errors.Wrapf(err, "cluster access for %s", name)
		return
	}
	kubectl, err := i.getKubectlPath()
	if err != nil {
		err = errors.Wrapf(err, "kubectl not found %s", name)
		return
	}
	kargs = append([]string{kubectl}, kargs...)
	return i.Capture(name, false, input, kargs...)
}
