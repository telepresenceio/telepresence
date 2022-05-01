package trafficmgr

import (
	"context"

	core "k8s.io/api/core/v1"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

func (tm *TrafficManager) IngressInfos(c context.Context) ([]*manager.IngressInfo, error) {
	tm.insLock.Lock()
	defer tm.insLock.Unlock()

	ingressInfo := tm.ingressInfo
	if ingressInfo == nil {
		tm.insLock.Unlock()
		ingressInfo, err := tm.detectIngressBehavior(c)
		if err != nil {
			return nil, err
		}
		tm.insLock.Lock()
		tm.ingressInfo = ingressInfo
	}
	is := make([]*manager.IngressInfo, len(tm.ingressInfo))
	copy(is, tm.ingressInfo)
	return is, nil
}

func (tm *TrafficManager) detectIngressBehavior(c context.Context) ([]*manager.IngressInfo, error) {
	loadBalancers, err := tm.findAllSvcByType(c, core.ServiceTypeLoadBalancer)
	if err != nil {
		return nil, err
	}
	type portFilter func(p *core.ServicePort) bool
	findTCPPort := func(ports []core.ServicePort, filter portFilter) *core.ServicePort {
		for i := range ports {
			p := &ports[i]
			if p.Protocol == "" || p.Protocol == "TCP" && filter(p) {
				return p
			}
		}
		return nil
	}

	// filters in priority order.
	portFilters := []portFilter{
		func(p *core.ServicePort) bool { return p.Name == "https" },
		func(p *core.ServicePort) bool { return p.Port == 443 },
		func(p *core.ServicePort) bool { return p.Name == "http" },
		func(p *core.ServicePort) bool { return p.Port == 80 },
		func(p *core.ServicePort) bool { return true },
	}

	iis := make([]*manager.IngressInfo, 0, len(loadBalancers))
	for _, lb := range loadBalancers {
		spec := &lb.Spec
		var port *core.ServicePort
		for _, pf := range portFilters {
			if p := findTCPPort(spec.Ports, pf); p != nil {
				port = p
				break
			}
		}
		if port == nil {
			continue
		}

		iis = append(iis, &manager.IngressInfo{
			Host:   lb.Name + "." + lb.Namespace,
			UseTls: port.Port == 443,
			Port:   port.Port,
		})
	}
	return iis, nil
}

// findAllSvcByType finds services with the given service type in all namespaces of the cluster returns
// a slice containing a copy of those services.
func (tm *TrafficManager) findAllSvcByType(c context.Context, svcType core.ServiceType) ([]*core.Service, error) {
	// NOTE: This is expensive in terms of bandwidth on a large cluster. We currently only use this
	// to retrieve ingress info and that task could be moved to the traffic-manager instead.
	var typedSvcs []*core.Service
	findTyped := func(ns string) error {
		ss, err := k8sapi.Services(c, ns, nil)
		if err != nil {
			return err
		}
		for _, s := range ss {
			si, _ := k8sapi.ServiceImpl(s)
			if si.Spec.Type == svcType {
				typedSvcs = append(typedSvcs, si)
			}
		}
		return nil
	}

	mns := tm.GetCurrentNamespaces(true)
	if len(mns) > 0 {
		for _, ns := range mns {
			if err := findTyped(ns); err != nil {
				return nil, err
			}
		}
	} else if err := findTyped(""); err != nil {
		return nil, err
	}
	return typedSvcs, nil
}
