package rt

import (
	"fmt"
	"os"
	"path/filepath"

	corev1 "k8s.io/api/core/v1"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
)

// coverHostPath is the node-local directory the traffic-manager (and, once
// wired, the traffic-agent) writes GOCOVERDIR counter data to. It is a
// hostPath volume because rtest clusters are single-node kind/minikube; see
// "Code coverage" in docs/plans/regression-test-framework/plan.md.
const coverHostPath = "/rtest-coverage"

// coverVolumeName is the volume/volumeMount name for coverHostPath on the
// traffic-manager deployment.
const coverVolumeName = "rtest-coverage"

// coverScraperPod is the name of the throwaway pod used to retrieve covdata
// off the node after the traffic-manager pod has flushed it on shutdown.
const coverScraperPod = "rtest-coverage-scraper"

// coverChmodManifest is a one-shot root pod that makes coverHostPath
// world-writable: kubelet creates a hostPath as root:root 0755, and the
// traffic-manager runs as uid 1000, so without this every exit-time counter
// flush fails silently. It runs once, at the first cover-mode manager
// provision, so mid-run manager restarts flush too.
const coverChmodManifest = `apiVersion: v1
kind: Pod
metadata:
  name: %[1]s
  namespace: %[2]s
  labels:
    purpose: tp-rtest
spec:
  restartPolicy: Never
  containers:
    - name: chmod
      image: busybox
      command: ["chmod", "0777", "%[3]s"]
      securityContext:
        runAsUser: 0
      volumeMounts:
        - name: coverage
          mountPath: %[3]s
  volumes:
    - name: coverage
      hostPath:
        path: %[3]s
        type: DirectoryOrCreate
`

// ensureCoverDirWritable runs the chmod pod once per run, before the first
// cover-instrumented manager pod could ever exit and try to flush.
func (r *Runtime) ensureCoverDirWritable(e Env, ns string) {
	if r.coverDirReady {
		return
	}
	r.coverDirReady = true
	name := "rtest-cover-chmod"
	manifest := fmt.Sprintf(coverChmodManifest, name, ns, coverHostPath)
	if err := r.applyManifest(e.Ctx, ns, "cover-chmod", manifest); err != nil {
		r.Infof("[rtest] cover: chmod pod: %v", err)
		return
	}
	_, _ = r.Kubectl(e.Ctx, ns, "wait", "pod/"+name, "--for=jsonpath={.status.phase}=Succeeded", "--timeout=60s")
	_, _ = r.Kubectl(e.Ctx, ns, "delete", "pod", name, "--ignore-not-found", "--wait=false")
}

// coverScraperManifest is a minimal pod that mounts coverHostPath so its
// contents can be `kubectl cp`'d out. busybox provides the `tar` binary
// kubectl cp needs and is already used by this chart's own hooks.
const coverScraperManifest = `apiVersion: v1
kind: Pod
metadata:
  name: %[1]s
  namespace: %[2]s
  labels:
    purpose: tp-rtest
spec:
  restartPolicy: Never
  containers:
    - name: scraper
      image: busybox
      command: ["sleep", "3600"]
      volumeMounts:
        - name: coverage
          mountPath: %[3]s
  volumes:
    - name: coverage
      hostPath:
        path: %[3]s
        type: DirectoryOrCreate
`

// coverClientDir returns build-output/rtest/coverage/client, creating it,
// regardless of RTEST_COVER: a cover-instrumented client binary warns on
// stderr when GOCOVERDIR is unset, so the variable is always provided and a
// plain binary simply ignores it. ok is false only when the directory can't
// be created, in which case the caller falls back to any ambient GOCOVERDIR.
func (r *Runtime) coverClientDir() (string, bool) {
	dir := filepath.Join(r.buildOutput, "rtest", "coverage", "client")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		r.Infof("[rtest] cover: creating %s: %v", dir, err)
		return "", false
	}
	return dir, true
}

// applyCoverManagerValues adds the GOCOVERDIR env var and the hostPath
// volume/volumeMount pair that make the traffic-manager flush coverage
// counters to coverHostPath on its graceful shutdown.
func applyCoverManagerValues(v managers.Values) managers.Values {
	dirType := corev1.HostPathDirectoryOrCreate
	v.ExtraEnv = append(v.ExtraEnv, corev1.EnvVar{Name: "GOCOVERDIR", Value: coverHostPath})
	v.ExtraVolumes = append(v.ExtraVolumes, corev1.Volume{
		Name: coverVolumeName,
		VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{Path: coverHostPath, Type: &dirType},
		},
	})
	v.ExtraVolumeMounts = append(v.ExtraVolumeMounts, corev1.VolumeMount{
		Name:      coverVolumeName,
		MountPath: coverHostPath,
	})
	return v
}

// collectManagerCoverage runs at the end of a coverage-mode run, before
// fixture teardown: it deletes the traffic-manager pod so Go's runtime
// flushes counter data to the node on its graceful SIGTERM shutdown, waits
// for the replacement (or a scale-down) to settle, then scrapes the
// hostPath through a throwaway busybox pod into
// build-output/rtest/coverage/manager. Every step logs and returns on
// error rather than failing the run: coverage collection is best-effort.
func (r *Runtime) collectManagerCoverage() {
	ctx := r.ctx
	ns := managers.ManagerNamespace

	if _, err := r.Kubectl(ctx, ns, "get", "deploy", helmReleaseName); err != nil {
		r.Infof("[rtest] cover: no manager release in %s, skipping manager coverage collection", ns)
		return
	}

	r.Infof("[rtest] cover: deleting traffic-manager pod to flush coverage data")
	if _, err := r.Kubectl(ctx, ns, "delete", "pod", "-l", "app=traffic-manager", "--wait"); err != nil {
		r.Infof("[rtest] cover: deleting traffic-manager pod: %v", err)
		return
	}
	if _, err := r.Kubectl(ctx, ns, "rollout", "status", "deploy/"+helmReleaseName, "--timeout=120s"); err != nil {
		r.Infof("[rtest] cover: waiting for traffic-manager rollout: %v", err)
		return
	}

	dstDir := filepath.Join(r.buildOutput, "rtest", "coverage", "manager")
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		r.Infof("[rtest] cover: creating %s: %v", dstDir, err)
		return
	}

	r.Infof("[rtest] cover: starting coverage scraper pod")
	manifest := fmt.Sprintf(coverScraperManifest, coverScraperPod, ns, coverHostPath)
	if err := r.applyManifest(ctx, ns, "coverage-scraper", manifest); err != nil {
		r.Infof("[rtest] cover: starting scraper pod: %v", err)
		return
	}
	defer func() {
		if _, err := r.Kubectl(ctx, ns, "delete", "pod", coverScraperPod, "--ignore-not-found", "--wait=false"); err != nil {
			r.Infof("[rtest] cover: deleting scraper pod: %v", err)
		}
	}()

	waitArgs := []string{"wait", "pod/" + coverScraperPod, "--for=condition=Ready", "--timeout=60s"}
	if _, err := r.Kubectl(ctx, ns, waitArgs...); err != nil {
		r.Infof("[rtest] cover: waiting for scraper pod: %v", err)
		return
	}

	r.Infof("[rtest] cover: copying manager coverage data to %s", dstDir)
	if _, err := r.Kubectl(ctx, ns, "cp", coverScraperPod+":"+coverHostPath, dstDir); err != nil {
		r.Infof("[rtest] cover: kubectl cp: %v", err)
		return
	}
	r.Infof("[rtest] cover: manager coverage collected")
}
