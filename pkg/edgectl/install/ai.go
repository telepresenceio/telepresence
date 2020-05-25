package edgectl

import (
	"fmt"
	"io/ioutil"
	"strings"
	"time"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// see https://github.com/datawire/ambassador-operator/blob/master/pkg/apis/getambassador/v2/ambassadorinstallation_types.go

const (
	// OSS flavor to set in DeployedRelease.flavor
	flavorOSS = "OSS"

	// AES flavor to set in DeployedRelease.flavor
	flavorAES = "AES"
)

// UnstructuredAmbassadorInstallation is an unstructured version of the
// AmbassadorInstallation CRD.
// Instead of importing the AmbassadorInstallation from the Operator, we keep
// it as an Unstructured, so we remove this problematic dependency...
type UnstructuredAmbassadorInstallation struct {
	unstructured.Unstructured
}

// findAmbassadorInstallation tries to find an existing AmbassadorInstallation
func findAmbassadorInstallation(kubectl Kubectl) (*UnstructuredAmbassadorInstallation, error) {
	ais, err := kubectl.WithStdout(ioutil.Discard).List("ambassadorinstallations", defInstallNamespace, []string{})
	if ais == nil {
		return nil, err
	}

	var items []unstructured.Unstructured
	if !ais.IsList() {
		items = []unstructured.Unstructured{*ais}
	} else {
		l, err := ais.ToList()
		if err != nil {
			return nil, err
		}
		items = l.Items
	}

	if len(items) == 0 {
		return nil, nil
	}

	return &UnstructuredAmbassadorInstallation{items[0]}, nil
}

func (u UnstructuredAmbassadorInstallation) IsEmpty() bool {
	_, _, err := unstructured.NestedMap(u.Object, "status")
	if err != nil {
		return true
	}
	return false
}

func (u UnstructuredAmbassadorInstallation) IsInstalled() bool {
	fullPath := []string{"status", "deployedRelease"}
	deployedRelease, found, err := unstructured.NestedFieldCopy(u.Object, fullPath...)
	if err != nil {
		return false
	}
	if !found || deployedRelease == nil {
		return false
	}
	return true
}

func (u UnstructuredAmbassadorInstallation) GetInstalledVersion() (string, error) {
	fullPath := []string{"status", "deployedRelease", "appVersion"}
	appVersion, found, err := unstructured.NestedString(u.Object, fullPath...)
	if err != nil {
		return "", err
	}
	if !found || appVersion == "" {
		return "", nil
	}
	return appVersion, nil
}

func (u UnstructuredAmbassadorInstallation) GetInstallOSS() bool {
	fullPath := []string{"spec", "installOSS"}
	installOSS, found, err := unstructured.NestedBool(u.Object, fullPath...)
	if err != nil {
		return false
	}
	if !found {
		return false
	}
	return installOSS
}

func (u UnstructuredAmbassadorInstallation) GetConditions() []map[string]interface{} {
	fullPath := []string{"status", "conditions"}
	conditions, found, err := unstructured.NestedSlice(u.Object, fullPath...)
	if err != nil {
		return nil
	}
	if !found {
		return nil
	}

	res := []map[string]interface{}{}
	for _, c := range conditions {
		res = append(res, c.(map[string]interface{}))
	}
	return res
}

// LastCondition returns the last condition
func (u UnstructuredAmbassadorInstallation) LastCondition() map[string]interface{} {
	last := map[string]interface{}{}
	lastTime := time.Time{}

	for _, c := range u.GetConditions() {
		cTimeStr := c["lastTransitionTime"].(string)
		cTime, err := time.Parse(time.RFC3339, cTimeStr)
		if err != nil {
			continue
		}
		if lastTime.IsZero() {
			last = c
			lastTime = cTime
		} else if cTime.After(lastTime) {
			last = c
		}
	}
	return last
}

func (u UnstructuredAmbassadorInstallation) LastConditionExplain() (string, string) {
	reason := ""
	message := ""

	cond := u.LastCondition()
	if r, ok := cond["reason"]; ok {
		reason = r.(string)
		if m, ok := cond["message"]; ok {
			message = m.(string)
		}
	}
	return reason, message
}

func (u UnstructuredAmbassadorInstallation) SetInstallOSS(installOSS bool) error {
	fullPath := []string{"spec", "installOSS"}
	if !installOSS {
		unstructured.RemoveNestedField(u.Object, fullPath...)
		return nil
	}
	return unstructured.SetNestedField(u.Object, true, fullPath...)
}

func (u UnstructuredAmbassadorInstallation) GetFlavor() (string, error) {
	fullPath := []string{"status", "deployedRelease", "flavor"}
	flavor, found, err := unstructured.NestedString(u.Object, fullPath...)
	if err != nil {
		return "", err
	}
	if !found {
		return "", nil
	}
	return flavor, nil
}

// checkAmbInstWithFlavor checks that 1) exists an AmbassadorInstallation
// installed 2) with the given flavor.
func checkAmbInstWithFlavor(kubectl Kubectl, flavor string) error {
	ambInstallation, err := findAmbassadorInstallation(kubectl)
	if err != nil {
		return err
	}

	if ambInstallation.IsEmpty() {
		return errors.New("no AmbassadorInstallation found")
	}

	if !ambInstallation.IsInstalled() {
		return errors.New("AmbassadorInstallation is not installed")
	}

	reason, message := ambInstallation.LastConditionExplain()
	if strings.Contains(reason, "Error") {
		return LoopFailedError(message)
	}

	f, err := ambInstallation.GetFlavor()
	if err != nil {
		return err
	}

	if f != flavor {
		return errors.New(fmt.Sprintf("AmbassadorInstallation is not a %s installation", flavor))
	}
	return nil
}
