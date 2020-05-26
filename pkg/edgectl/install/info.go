package edgectl

import (
	"io/ioutil"
	"regexp"
	"strings"

	"helm.sh/helm/v3/pkg/strvals"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
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

func NewClusterInfo(kubectl Kubectl) clusterInfo {
	// Try to determine cluster type from node labels
	nodesList, err := kubectl.WithStdout(ioutil.Discard).List("nodes", "", []string{})
	if err != nil {
		// ignore errors if we cannot detect the cluster type
		return clusterInfo{}
	}
	nodes, err := nodesList.ToList()
	if nodes == nil {
		return clusterInfo{}
	}
	items := nodes.Items
	if len(items) == 0 {
		return clusterInfo{}
	}

	return newClusterInfoFromNodeLabels(nodes.Items[0].GetLabels())
}

func newClusterInfoFromNodeLabels(clusterNodeLabels map[string]string) clusterInfo {
	for _, label := range clusterNodeLabels {
		if strings.Contains(label, "docker-desktop") {
			return clusterInfoDatabase[clusterDockerDesktop]
		} else if strings.Contains(label, "minikube") {
			return clusterInfoDatabase[clusterMinikube]
		} else if strings.Contains(label, "kind") {
			return clusterInfoDatabase[clusterKIND]
		} else if strings.Contains(label, "k3d") {
			return clusterInfoDatabase[clusterK3D]
		} else if strings.Contains(label, "gke") {
			return clusterInfoDatabase[clusterGKE]
		} else if strings.Contains(label, "aks") {
			return clusterInfoDatabase[clusterAKS]
		} else if strings.Contains(label, "compute") {
			return clusterInfoDatabase[clusterEKS]
		} else if strings.Contains(label, "ec2") {
			return clusterInfoDatabase[clusterEC2]
		}
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
	instOperatorKIND
	instOperatorKOPS
	instOperatorKubespray
	instOperatorMinikube
	instOperatorK3S
	instHelm
)

var (
	regexAOSSImage = regexp.MustCompile(`\bdatawire/ambassador:(\S+)`)
	regexAESImage = regexp.MustCompile(`\bdatawire/aes:(\S+)`)
)

type installationMethodInfo struct {
	Method   int
	Label    string
	Name     string
	LongName string
	Image    *regexp.Regexp
	Namespace string
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
		Image:     regexAESImage,
		Namespace: defInstallNamespace,
	},
	{
		Method:    instOperatorKIND,
		Label:     "app.kubernetes.io/name=ambassador,app.kubernetes.io/managed-by=amb-oper-kind",
		Name:      "operator",
		LongName:  "the Ambassador Operator (in KIND)",
		Image:     regexAOSSImage,
		Namespace: defInstallNamespace,
	},
	{
		Method:    instOperatorK3S,
		Label:     "app.kubernetes.io/name=ambassador,app.kubernetes.io/managed-by=amb-oper-k3s",
		Name:      "operator",
		LongName:  "the Ambassador Operator (in K3S)",
		Image:     regexAOSSImage,
		Namespace: defInstallNamespace,
	},
	{
		Method:    instOperatorMinikube,
		Label:     "app.kubernetes.io/name=ambassador,app.kubernetes.io/managed-by=amb-oper-minikube",
		Name:      "operator",
		LongName:  "the Ambassador Operator (in Minikube)",
		Image:     regexAOSSImage,
		Namespace: defInstallNamespace,
	},
	{
		Method:    instOperatorKOPS,
		Label:     "app.kubernetes.io/name=ambassador,app.kubernetes.io/managed-by=amb-oper-kops",
		Name:      "operator",
		LongName:  "the Ambassador Operator (in KOPS)",
		Image:     regexAOSSImage,
		Namespace: defInstallNamespace,
	},
	{
		Method:    instOperatorKubespray,
		Label:     "app.kubernetes.io/name=ambassador,app.kubernetes.io/managed-by=amb-oper-kubespray",
		Name:      "operator",
		LongName:  "the Ambassador Operator (in Kubespray)",
		Image:     regexAOSSImage,
		Namespace: defInstallNamespace,
	},
	{
		Method:   instOperator,
		Label:    "app.kubernetes.io/name=ambassador,app.kubernetes.io/managed-by in (amb-oper,amb-oper-manifest,amb-oper-helm,amb-oper-azure)",
		Name:     "operator",
		LongName: "the Ambassador Operator",
		Image:     regexAESImage,
		Namespace: defInstallNamespace,
	},
	{
		Method:   instHelm,
		Label:    "app.kubernetes.io/name=ambassador",
		Name:     "helm",
		LongName: "Helm",
		Image:     regexAESImage,
		Namespace: defInstallNamespace,
	},
	{
		Method:   instAES,
		Label:    "product=aes",
		Name:     "aes",
		LongName: "AES manifests",
		Image:     regexAESImage,
		Namespace: defInstallNamespace,
	},
	{
		Method:   instOSS,
		Label:    "service=ambassador",
		Name:     "oss",
		LongName: "OSS manifests",
		Image:     regexAOSSImage,
		Namespace: "default",
	},
}

// getExistingInstallation tries to find an existing deployment by looking at a list of predefined labels,
// If such a deployment is found, it returns the image and the installation "family" (aes, oss, helm, etc).
// It returns an empty string if no installation could be found.
//
// TODO: Try to search all namespaces (which may fail due to RBAC) and capture a
//       correct namespace for an Ambassador installation (what if there is more than
//       one?), then proceed operating on that Ambassador in that namespace. Right now
//       we hard-code the "ambassador" namespace in a number of spots.
//
func getExistingInstallation(kubectl Kubectl) (string, installationMethodInfo, error) {
	findFor := func(label string, imageRe *regexp.Regexp, namespace string) (string, error) {
		deploys, err := kubectl.WithStdout(ioutil.Discard).List("deployments", namespace, []string{label})
		if deploys == nil {
			return "", err
		}
		var items []unstructured.Unstructured
		if !deploys.IsList() {
			items = []unstructured.Unstructured{*deploys}
		} else {
			l, err := deploys.ToList()
			if err != nil {
				return "", err
			}
			items = l.Items
		}
		for _, deploy := range items {
			deployment := appsv1.Deployment{}
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(deploy.UnstructuredContent(), &deployment); err != nil {
				continue
			}
			containers := deployment.Spec.Template.Spec.Containers
			for _, container := range containers {
				if matches := imageRe.FindStringSubmatch(container.Image); len(matches) == 2 {
					return matches[1], nil
				}
			}
		}
		return "", nil
	}

	for _, info := range defInstallationMethodsInfo {
		if info.Label == "" {
			continue
		}
		version, err := findFor(info.Label, info.Image, info.Namespace)
		if err != nil {
			continue // ignore errors
		}
		if version != "" {
			return version, info, nil
		}
	}

	return "", installationMethodInfo{Method: instNone}, nil
}
