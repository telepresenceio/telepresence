module github.com/telepresenceio/telepresence/v2/pkg/dnet/testdata/mockserver

go 1.19

require (
	github.com/datawire/dlib v1.3.1
	k8s.io/apimachinery v0.27.1
	k8s.io/kubernetes v1.27.1
)

require (
	github.com/go-logr/logr v1.2.4 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/google/gofuzz v1.2.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/moby/spdystream v0.2.0 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/sirupsen/logrus v1.9.0 // indirect
	golang.org/x/net v0.10.0 // indirect
	golang.org/x/sys v0.8.0 // indirect
	golang.org/x/text v0.9.0 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
	k8s.io/api v0.27.1 // indirect
	k8s.io/apiserver v0.27.1 // indirect
	k8s.io/klog/v2 v2.100.1 // indirect
	k8s.io/utils v0.0.0-20230505201702-9f6742963106 // indirect
	sigs.k8s.io/json v0.0.0-20221116044647-bc3834ca7abd // indirect
	sigs.k8s.io/structured-merge-diff/v4 v4.2.3 // indirect
	sigs.k8s.io/yaml v1.3.0 // indirect
)

// Because we (unfortunately) need to require k8s.io/kubernetes, which
// is (unfortunately) managed in a way that makes it hostile to being
// used as a library (see
// https://news.ycombinator.com/item?id=27827389) we need to provide
// replacements for a bunch of k8s.io modules that it refers to by
// bogus/broken v0.0.0 versions.
replace (
	k8s.io/api => k8s.io/api v0.27.1
	k8s.io/apiextensions-apiserver => k8s.io/apiextensions-apiserver v0.27.1
	k8s.io/apimachinery => k8s.io/apimachinery v0.27.1
	k8s.io/apiserver => k8s.io/apiserver v0.27.1
	k8s.io/cli-runtime => k8s.io/cli-runtime v0.27.1
	k8s.io/client-go => k8s.io/client-go v0.27.1
	k8s.io/cloud-provider => k8s.io/cloud-provider v0.27.1
	k8s.io/cluster-bootstrap => k8s.io/cluster-bootstrap v0.27.1
	k8s.io/code-generator => k8s.io/code-generator v0.27.1
	k8s.io/component-base => k8s.io/component-base v0.27.1
	k8s.io/component-helpers => k8s.io/component-helpers v0.27.1
	k8s.io/controller-manager => k8s.io/controller-manager v0.27.1
	k8s.io/cri-api => k8s.io/cri-api v0.27.1
	k8s.io/csi-translation-lib => k8s.io/csi-translation-lib v0.27.1
	k8s.io/dynamic-resource-allocation => k8s.io/dynamic-resource-allocation v0.27.1
	k8s.io/kube-aggregator => k8s.io/kube-aggregator v0.27.1
	k8s.io/kube-controller-manager => k8s.io/kube-controller-manager v0.27.1
	k8s.io/kube-proxy => k8s.io/kube-proxy v0.27.1
	k8s.io/kube-scheduler => k8s.io/kube-scheduler v0.27.1
	k8s.io/kubectl => k8s.io/kubectl v0.27.1
	k8s.io/kubelet => k8s.io/kubelet v0.27.1
	k8s.io/kubernetes => k8s.io/kubernetes v1.27.1
	k8s.io/legacy-cloud-providers => k8s.io/legacy-cloud-providers v0.27.1
	k8s.io/metrics => k8s.io/metrics v0.27.1
	k8s.io/mount-utils => k8s.io/mount-utils v0.27.1
	k8s.io/pod-security-admission => k8s.io/pod-security-admission v0.27.1
	k8s.io/sample-apiserver => k8s.io/sample-apiserver v0.27.1
)
