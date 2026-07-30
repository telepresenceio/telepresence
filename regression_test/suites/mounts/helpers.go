package mounts

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// mountTimeout bounds every ordinary EventuallyFile poll in this package: a
// FUSE/SFTP intercept mount's content appears asynchronously, wired up after
// the intercept itself is already reported ACTIVE.
const mountTimeout = 30 * time.Second

// tokenRelPath is the serviceaccount token's path under a pod's filesystem
// root, present on any pod that hasn't disabled automountServiceAccountToken
// -- a reliable anchor for "the mount is actually up", independent of the
// ConfigVolume this package's suites add (mirrors integration_test/
// mounts_test.go's Test_CollidingMounts, which stats the same path).
const tokenRelPath = "var/run/secrets/kubernetes.io/serviceaccount/token"

// configVolumeK8sName is the Kubernetes Volume name workloads.
// EchoWithConfigVolume's deployment.yaml template hardcodes for the
// ConfigMap volume (regression_test/framework/workloads/templates/
// deployment.yaml: "name: rtest-config" under both the container's
// volumeMounts and the pod spec's volumes) -- distinct from
// workloads.ConfigVolume.Name, which is the ConfigMap *object's* name
// ("<workload>-config"). The inject-ignore-volume-mounts annotation matches
// by Kubernetes Volume name (pkg/types/mountpolicy.go's MountPolicies.Get:
// key == volumeName || strings.HasPrefix(mountPath, key)), so this constant,
// not workloads.ConfigVolume.Name, is the annotation value Ignored needs.
// The workloads package exports no equivalent constant (API gap).
const configVolumeK8sName = "rtest-config"

// envInterceptMounts is the attach environment key naming the colon-joined
// list of remote mount paths the agent actually mounts (cmd/traffic/cmd/
// agent/agent.go's buildEnv; pkg/agentconfig/sidecar.go's
// EnvInterceptMounts). A path with mount policy MountPolicyIgnore is
// dropped from this list before it ever reaches the client.
const envInterceptMounts = "TELEPRESENCE_MOUNTS"

// configFilePath is the local path the mounted ConfigMap file appears at
// under root (rt.MountRoot's TELEPRESENCE_ROOT).
func configFilePath(root string) string {
	return filepath.Join(root, workloads.ConfigVolumeMountPath, workloads.ConfigVolumeFileName)
}

// tokenFilePath is the local path of the serviceaccount token file under
// root.
func tokenFilePath(root string) string {
	return filepath.Join(root, filepath.FromSlash(tokenRelPath))
}

// isConfigContent is an EventuallyFile predicate matching the ConfigVolume's
// exact, fixed content (workloads.ConfigVolumeContent has no trailing
// newline; a ConfigMap value is stored and mounted byte-for-byte).
func isConfigContent(b []byte) bool {
	return string(b) == workloads.ConfigVolumeContent
}

// isNonEmpty is an EventuallyFile predicate for "readable and has content":
// used for the serviceaccount token file, whose exact value isn't fixed.
func isNonEmpty(b []byte) bool {
	return len(b) > 0
}

// mountedPaths splits a's TELEPRESENCE_MOUNTS entry into its colon-separated
// remote paths, or nil if the attach carries no such entry.
func mountedPaths(a *rt.Attach) []string {
	if a.Intercept == nil {
		return nil
	}
	v := a.Intercept.Environment[envInterceptMounts]
	if v == "" {
		return nil
	}
	return strings.Split(v, ":")
}

// containsPath reports whether paths contains path exactly.
func containsPath(paths []string, path string) bool {
	for _, p := range paths {
		if p == path {
			return true
		}
	}
	return false
}
