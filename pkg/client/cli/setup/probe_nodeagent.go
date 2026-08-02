package setup

import (
	"context"
	"fmt"
	"sort"
	"strings"

	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// probeNodeAgent is P3: node-agent viability, including the admission canary.
func (p *Prober) probeNodeAgent(ctx context.Context, nodes []corev1.Node, nodesErr error, provider string, nsExists bool) NodeAgentFacts {
	facts := NodeAgentFacts{}
	if nodesErr != nil {
		facts.Viable = Finding{Verdict: VerdictUnknown, Evidence: []string{nodesErr.Error()}}
		return facts
	}

	facts.TotalNodes = len(nodes)
	runtimeSet := map[string]bool{}
	for _, n := range nodes {
		if n.Labels["kubernetes.io/os"] == "linux" {
			facts.LinuxNodes++
		}
		if scheme := runtimeScheme(n.Status.NodeInfo.ContainerRuntimeVersion); scheme != "" {
			runtimeSet[scheme] = true
		}
		if n.Labels["cloud.google.com/gke-autopilot"] == "true" {
			facts.Autopilot = true
		}
		if provider == "gke" && strings.HasPrefix(n.Name, "gk3-") {
			facts.Autopilot = true
		}
	}
	runtimes := make([]string, 0, len(runtimeSet))
	for r := range runtimeSet {
		runtimes = append(runtimes, r)
	}
	sort.Strings(runtimes)
	facts.Runtimes = runtimes

	if facts.Autopilot {
		facts.Viable = Finding{Verdict: VerdictNo, Evidence: []string{"GKE Autopilot forbids hostPID"}}
		return facts
	}
	if facts.LinuxNodes == 0 {
		facts.Viable = Finding{Verdict: VerdictNo, Evidence: []string{"no Linux nodes found"}}
		return facts
	}

	if supported, evidence := runtimeSupport(runtimes); !supported {
		facts.Viable = Finding{Verdict: VerdictProbable, Evidence: evidence}
		return facts
	}

	if !nsExists {
		facts.Viable = Finding{
			Verdict:  VerdictProbable,
			Evidence: []string{"manager namespace does not exist; admission canary skipped"},
		}
		return facts
	}

	// A dry-run pod create needs create access; without it, a Forbidden from the
	// canary would be an RBAC denial, indistinguishable from an admission denial.
	canCreate := p.singleAccessCheck(ctx, &authv1.ResourceAttributes{
		Verb:      "create",
		Resource:  "pods",
		Namespace: p.ManagerNamespace,
	})
	if canCreate.Verdict != VerdictYes {
		facts.Viable = Finding{
			Verdict:  VerdictProbable,
			Evidence: append([]string{"insufficient RBAC to run the admission canary"}, canCreate.Evidence...),
		}
		return facts
	}

	viable, denial := p.admissionCanary(ctx)
	facts.Viable = viable
	facts.CanaryDenial = denial
	return facts
}

// runtimeScheme returns the part of a container-runtime version string before
// "://", e.g. "containerd" from "containerd://1.6.6". A string without the
// delimiter is returned whole, so it surfaces verbatim in the unrecognized-
// runtime evidence instead of being silently misparsed.
func runtimeScheme(v string) string {
	scheme, _, _ := strings.Cut(v, "://")
	return scheme
}

// runtimeSupport reports whether every observed runtime is fully supported
// (containerd, cri-o) and, when not, the evidence explaining what each
// unsupported runtime needs.
func runtimeSupport(runtimes []string) (bool, []string) {
	var evidence []string
	supported := true
	for _, r := range runtimes {
		switch r {
		case "containerd", "cri-o":
		case "docker":
			supported = false
			evidence = append(evidence, "docker runtime detected; node-agent needs a cri-dockerd socket (nodeAgent.criSocket)")
		default:
			supported = false
			evidence = append(evidence, fmt.Sprintf("unrecognized container runtime %q; set nodeAgent.criSocket explicitly", r))
		}
	}
	return supported, evidence
}

// admissionCanary performs a server-side dry-run create of a minimal
// node-agent-shaped Pod in the manager namespace, exercising Pod Security
// admission, Autopilot, and any policy engine in one shot.
func (p *Prober) admissionCanary(ctx context.Context) (Finding, string) {
	hostPathType := corev1.HostPathDirectory
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "tel-setup-canary-",
			Labels:       map[string]string{"app.kubernetes.io/created-by": "telepresence-setup"},
		},
		Spec: corev1.PodSpec{
			HostPID:       true,
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:    "canary",
					Image:   "busybox",
					Command: []string{"true"},
					SecurityContext: &corev1.SecurityContext{
						Capabilities: &corev1.Capabilities{
							Add: []corev1.Capability{"SYS_ADMIN", "SYS_PTRACE", "NET_ADMIN", "NET_RAW"},
						},
					},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "run", MountPath: "/run", ReadOnly: true},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "run",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{Path: "/run", Type: &hostPathType},
					},
				},
			},
		},
	}

	_, err := p.KubeClient.CoreV1().Pods(p.ManagerNamespace).Create(ctx, pod, metav1.CreateOptions{
		DryRun: []string{metav1.DryRunAll},
	})
	switch {
	case err == nil:
		return Finding{Verdict: VerdictYes}, ""
	case apierrors.IsForbidden(err):
		return Finding{Verdict: VerdictNo}, err.Error()
	default:
		return Finding{Verdict: VerdictUnknown, Evidence: []string{err.Error()}}, ""
	}
}
