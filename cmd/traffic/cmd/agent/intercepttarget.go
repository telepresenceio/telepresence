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

// InterceptTarget describes the mapping between service ports and one container port. All entries
// must be guaranteed to all have the same Protocol, ContainerPort, and AgentPort. The slice must
// be considered immutable once created using NewInterceptTarget.
type InterceptTarget []*agentconfig.Intercept

func NewInterceptTarget(ics []*agentconfig.Intercept) InterceptTarget {
	// This is a parameter assertion. If it is triggered, then something is dead wrong in the caller code.
	ni := len(ics)
	if ni == 0 {
		panic("attempt to add intercept create an InterceptTarget with no Intercepts")
	}
	if ni > 1 {
		sv := ics[0]
		for i := 1; i < ni; i++ {
			ic := ics[i]
			if sv.AgentPort != ic.AgentPort || sv.ContainerPort != ic.ContainerPort || sv.Protocol != ic.Protocol {
				panic("attempt to add intercept to an InterceptTarget with different AgentPort or ContainerPort")
			}
		}
	}
	return ics
}

func (cp InterceptTarget) MatchForSpec(spec *manager.InterceptSpec) bool {
	for _, sv := range cp {
		if agentconfig.SpecMatchesIntercept(spec, sv) {
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

func (cp InterceptTarget) AppProtocol(ctx context.Context) (proto string) {
	var foundSv *agentconfig.Intercept
	for _, sv := range cp {
		if sv.AppProtocol == "" {
			continue
		}
		if foundSv == nil {
			foundSv = sv
			proto = foundSv.AppProtocol
		} else if foundSv.AppProtocol != sv.AppProtocol {
			svcPort := func(s *agentconfig.Intercept) string {
				if s.ServicePortName != "" {
					return fmt.Sprintf("%s:%s", s.ServiceName, s.ServicePortName)
				}
				return fmt.Sprintf("%s:%d", s.ServiceName, s.ServicePort)
			}
			dlog.Warningf(ctx, "port %s appProtocol %s differs from port %s appProtocol %s. %s will be used for container port %d",
				svcPort(foundSv), proto,
				svcPort(sv), sv.AppProtocol,
				proto, sv.ContainerPort)
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
	for i, sv := range cp {
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
		sb.WriteString(sv.ServiceName)
		sb.WriteByte(':')
		if sv.ServicePortName != "" {
			sb.WriteString(sv.ServicePortName)
		} else {
			sb.WriteString(strconv.Itoa(int(sv.ServicePort)))
		}
	}
	if l > 1 {
		sb.WriteByte(']')
	}
	ioutil.Printf(&sb, " => container port %d/%s", cp.ContainerPort(), cp.Protocol())
	return sb.String()
}
