package agent

import (
	"bytes"
	"context"
	"fmt"
	"strconv"

	v1 "k8s.io/api/core/v1"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

// InterceptTarget describes the mapping between service ports and one container port, or if no service
// is used, just the container port.
// All entries must be guaranteed to all have the same Protocol, ContainerPort, and AgentPort.
// The slice must be considered immutable once created using NewInterceptTarget.
type InterceptTarget []*agentconfig.Intercept

func NewInterceptTarget(ics []*agentconfig.Intercept) InterceptTarget {
	// This is a parameter assertion. If it is triggered, then something is dead wrong in the caller code.
	ni := len(ics)
	if ni == 0 {
		panic("attempt to add intercept create an InterceptTarget with no Intercepts")
	}
	if ni > 1 {
		icZero := ics[0]
		for i := 1; i < ni; i++ {
			ic := ics[i]
			if icZero.AgentPort != ic.AgentPort || icZero.ContainerPort != ic.ContainerPort || icZero.Protocol != ic.Protocol {
				panic("attempt to add intercept to an InterceptTarget with different AgentPort or ContainerPort")
			}
		}
	}
	return ics
}

func (cp InterceptTarget) MatchForSpec(spec *manager.InterceptSpec) bool {
	for _, ic := range cp {
		if agentconfig.SpecMatchesIntercept(spec, ic) {
			return true
		}
	}
	return false
}

func (cp InterceptTarget) AgentPort() uint16 {
	return cp[0].AgentPort
}

func (cp InterceptTarget) ContainerPort() uint16 {
	return cp[0].ContainerPort
}

func (cp InterceptTarget) ContainerPortName() string {
	return cp[0].ContainerPortName
}

func (cp InterceptTarget) Protocol() v1.Protocol {
	return cp[0].Protocol
}

func portString(ic *agentconfig.Intercept) (s string) {
	if ic.ServiceUID != "" {
		p := ic.ServicePortName
		if p == "" {
			p = strconv.Itoa(int(ic.ServicePort))
		}
		return fmt.Sprintf("service port %s:%s", ic.ServiceName, p)
	}
	p := ic.ContainerPortName
	if p == "" {
		p = strconv.Itoa(int(ic.ContainerPort))
	}
	return fmt.Sprintf("container port %s", p)
}

func (cp InterceptTarget) AppProtocol(ctx context.Context) (proto string) {
	var foundIc *agentconfig.Intercept
	for _, ic := range cp {
		if ic.AppProtocol == "" {
			continue
		}
		if foundIc == nil {
			foundIc = ic
			proto = foundIc.AppProtocol
		} else if foundIc.AppProtocol != ic.AppProtocol {
			dlog.Warningf(ctx, "%s appProtocol %s differs from %s appProtocol %s. %s will be used for %s",
				portString(foundIc), proto,
				portString(ic), ic.AppProtocol,
				proto, portString(ic))
		}
	}
	return proto
}

func (cp InterceptTarget) HasServicePortName(name string) bool {
	for _, sv := range cp {
		if sv.ServicePortName == name {
			return true
		}
	}
	return false
}

func (cp InterceptTarget) HasServicePort(port uint16) bool {
	for _, sv := range cp {
		if sv.ServicePort == port {
			return true
		}
	}
	return false
}

func (cp InterceptTarget) String() string {
	sb := bytes.Buffer{}
	l := len(cp)
	if l > 1 {
		sb.WriteByte('[')
	}
	for i, ic := range cp {
		if i > 0 {
			switch l {
			case 2:
				sb.WriteString(" and ")
			case i + 1:
				sb.WriteString(", and ")
			default:
				sb.WriteString(", ")
			}
		}
		sb.WriteString(portString(ic))
	}
	if l > 1 {
		sb.WriteByte(']')
	}
	if l > 1 || cp[0].ServiceName != "" {
		ioutil.Printf(&sb, " => container port %d/%s", cp.ContainerPort(), cp.Protocol())
	}
	return sb.String()
}
