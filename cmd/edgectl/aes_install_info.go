package main

import (
	"bufio"
	"regexp"
	"strings"

	"helm.sh/helm/v3/pkg/strvals"
)

//
// cluster information
//

const (
	clusterUnknown = iota
	clusterDockerDesktop
	clusterMinikube
	clusterKIND
	clusterK3D
	clusterGKE
	clusterAKS
	clusterEKS
	clusterEC2
)

// clusterInfoMessages represent custom messages for some environments
type clusterInfoMessages struct {
	// getServiceIP is the message printed for how to get the service IP
	getServiceIP string
}

// clusterInfo describes some properties about the cluster where the installation is performed
type clusterInfo struct {
	// a name for this kind of cluster (ie, gke)
	name string

	// True if this is a local environment (ie, minikube)
	isLocal bool

	// extra Chart values to set in this environment, as a list of assignments
	chartValues []string

	// customMessages are some custom messages for this environment
	customMessages clusterInfoMessages
}

func (c clusterInfo) CopyChartValuesTo(chartValues map[string]interface{}) {
	for _, assignment := range c.chartValues {
		err := strvals.ParseInto(assignment, chartValues)
		if err != nil {
			panic(err) // this should never happen: only if we have created a wrong `chartValues`
		}
	}
}

// clusterInfoDatabase is the database of information about
var clusterInfoDatabase = map[int]clusterInfo{
	clusterUnknown: {
		name: "unknown",
	},
	clusterDockerDesktop: {
		name:    "docker-desktop",
		isLocal: true,
	},
	clusterMinikube: {
		name:    "minikube",
		isLocal: true,
		customMessages: clusterInfoMessages{
			getServiceIP: "minikube service -n ambassador ambassador",
		},
	},
	clusterKIND: {
		name:    "KIND",
		isLocal: true,
		chartValues: []string{
			"service.type=NodePort",
		},
	},
	clusterK3D: {
		name:    "K3D",
		isLocal: true,
		chartValues: []string{
			"service.type=NodePort",
		},
	},
	clusterGKE: {
		name: "GKE",
	},
	clusterAKS: {
		name: "AKS",
	},
	clusterEKS: {
		name: "EKS",
	},
	clusterEC2: {
		name: "AEC2",
	},
}

func newClusterInfoFromNodeLabels(clusterNodeLabels string) clusterInfo {
	if strings.Contains(clusterNodeLabels, "docker-desktop") {
		return clusterInfoDatabase[clusterDockerDesktop]
	} else if strings.Contains(clusterNodeLabels, "minikube") {
		return clusterInfoDatabase[clusterMinikube]
	} else if strings.Contains(clusterNodeLabels, "kind") {
		return clusterInfoDatabase[clusterKIND]
	} else if strings.Contains(clusterNodeLabels, "k3d") {
		return clusterInfoDatabase[clusterK3D]
	} else if strings.Contains(clusterNodeLabels, "gke") {
		return clusterInfoDatabase[clusterGKE]
	} else if strings.Contains(clusterNodeLabels, "aks") {
		return clusterInfoDatabase[clusterAKS]
	} else if strings.Contains(clusterNodeLabels, "compute") {
		return clusterInfoDatabase[clusterEKS]
	} else if strings.Contains(clusterNodeLabels, "ec2") {
		return clusterInfoDatabase[clusterEC2]
	}
	return clusterInfoDatabase[clusterUnknown]
}

//
// installation methods
//

const (
	instNone = iota
	instOSS
	instAES
	instEdgectl
	instOperator
	instHelm
)

type installationMethodInfo struct {
	Method   int
	Label    string
	Name     string
	LongName string
	Image    *regexp.Regexp
}

// defInstallationMethodsInfo contains information
// about different installation methods. It can be used for detecting previous
// installation methods.
// NOTE: this is an ordered-list: higher-precision labels are first
var defInstallationMethodsInfo = []installationMethodInfo{
	{
		Method:   instEdgectl,
		Label:    "app.kubernetes.io/managed-by=edgectl",
		Name:     "edgectl",
		LongName: "edgectl",
		Image:    regexp.MustCompile("quay[.]io/datawire/aes:([[:^space:]]+)"),
	},
	{
		Method:   instOperator,
		Label:    "app.kubernetes.io/name=ambassador,app.kubernetes.io/managed-by in (amb-oper,amb-oper-manifest,amb-oper-helm,amb-oper-azure)",
		Name:     "operator",
		LongName: "the Ambassador Operator",
		Image:    regexp.MustCompile("quay[.]io/datawire/aes:([[:^space:]]+)"),
	},
	{
		Method:   instHelm,
		Label:    "app.kubernetes.io/name=ambassador",
		Name:     "helm",
		LongName: "Helm",
		Image:    regexp.MustCompile("quay[.]io/datawire/aes:([[:^space:]]+)"),
	},
	{
		Method:   instAES,
		Label:    "product=aes",
		Name:     "aes",
		LongName: "AES manifests",
		Image:    regexp.MustCompile("quay[.]io/datawire/aes:([[:^space:]]+)"),
	},
	{
		Method:   instOSS,
		Label:    "service=ambassador",
		Name:     "oss",
		LongName: "OSS manifests",
		Image:    regexp.MustCompile("quay[.]io/datawire/ambassador:([[:^space:]]+)"),
	},
}

type deployGetter func(string) (string, error)

// getExistingInstallation tries to find an existing deployment by looking at a list of predefined labels,
// If such a deployment is found, it returns the image and the installation "family" (aes, oss, helm, etc).
// It returns an empty string if no installation could be found.
//
// TODO: Try to search all namespaces (which may fail due to RBAC) and capture a
//       correct namespace for an Ambassador installation (what if there is more than
//       one?), then proceed operating on that Ambassador in that namespace. Right now
//       we hard-code the "ambassador" namespace in a number of spots.
//
func getExistingInstallation(deploys deployGetter) (string, installationMethodInfo, error) {
	findFor := func(label string, imageRe *regexp.Regexp) (string, error) {
		deploys, err := deploys(label)
		if err != nil {
			return "", err
		}
		scanner := bufio.NewScanner(strings.NewReader(deploys))
		for scanner.Scan() {
			image := strings.TrimSpace(scanner.Text())
			if matches := imageRe.FindStringSubmatch(image); len(matches) == 2 {
				return matches[1], nil
			}
		}
		return "", scanner.Err()
	}

	for _, info := range defInstallationMethodsInfo {
		if info.Label == "" {
			continue
		}
		version, err := findFor(info.Label, info.Image)
		if err != nil {
			continue // ignore errors
		}
		if version != "" {
			return version, info, nil
		}
	}
	return "", installationMethodInfo{Method: instNone}, nil
}
