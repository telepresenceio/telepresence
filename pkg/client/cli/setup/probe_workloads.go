package setup

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/telepresenceio/clog"
)

const (
	workloadListLimit = 20
	workloadSampleMax = 3
)

// probeWorkloads samples Deployments in the workload namespace so the
// next-steps epilogue can name a real attach candidate. Denials and other
// listing failures leave the facts empty; the samples are a convenience, not
// a requirement.
func (p *Prober) probeWorkloads(ctx context.Context) WorkloadFacts {
	facts := WorkloadFacts{}
	if p.WorkloadNamespace == "" {
		return facts
	}
	list, err := p.KubeClient.AppsV1().Deployments(p.WorkloadNamespace).List(ctx, metav1.ListOptions{Limit: workloadListLimit})
	if err != nil {
		clog.Debugf(ctx, "unable to sample deployments in %s: %v", p.WorkloadNamespace, err)
		return facts
	}
	for i := range list.Items {
		d := &list.Items[i]
		// The chart's own workloads make no sense as attach examples.
		switch d.Name {
		case "traffic-manager", "quic-forwarder":
			continue
		}
		sample := WorkloadSample{Name: d.Name, Namespace: d.Namespace}
		if cns := d.Spec.Template.Spec.Containers; len(cns) > 0 && len(cns[0].Ports) > 0 {
			sample.Port = cns[0].Ports[0].ContainerPort
		}
		facts.Samples = append(facts.Samples, sample)
		if len(facts.Samples) == workloadSampleMax {
			break
		}
	}
	return facts
}
