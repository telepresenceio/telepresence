package install

import (
	"context"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/datawire/ambassador/pkg/kates"
)

// svcPortByNameOrNumber iterates through a list of ports in a service and
// only returns the ports that match the given nameOrNumber
func svcPortByNameOrNumber(svc *kates.Service, nameOrNumber string) []*kates.ServicePort {
	svcPorts := make([]*kates.ServicePort, 0)
	ports := svc.Spec.Ports
	var isName bool
	validName := validation.IsValidPortName(nameOrNumber)
	if len(validName) > 0 {
		isName = false
	} else {
		isName = true
	}
	for i := range ports {
		port := &ports[i]
		matchFound := false
		// If no nameOrNumber has been specified, we include it
		if nameOrNumber == "" {
			matchFound = true
		}
		// If the nameOrNumber is a valid name, we compare it to the
		// name listed in the servicePort
		if isName {
			if nameOrNumber == port.Name {
				matchFound = true
			}
		} else {
			// Otherwise we compare it to the port number
			givenPort, err := strconv.Atoi(nameOrNumber)
			if err == nil && int32(givenPort) == port.Port {
				matchFound = true
			}
		}
		if matchFound {
			svcPorts = append(svcPorts, port)
		}
	}
	return svcPorts
}

func FindMatchingServices(c context.Context, client *kates.Client, portNameOrNumber, svcName, namespace string, labels map[string]string) ([]*kates.Service, error) {
	// TODO: Expensive on large clusters but the problem goes away once we move the installer to the traffic-manager
	var svcs []*kates.Service
	if err := client.List(c, kates.Query{Name: svcName, Kind: "Service", Namespace: namespace}, &svcs); err != nil {
		return nil, err
	}

	// Returns true if selector is completely included in labels
	labelsMatch := func(selector map[string]string) bool {
		if len(selector) == 0 || len(labels) < len(selector) {
			return false
		}
		for k, v := range selector {
			if labels[k] != v {
				return false
			}
		}
		return true
	}

	var matching []*kates.Service
	for _, svc := range svcs {
		if (svcName == "" || svc.Name == svcName) && labelsMatch(svc.Spec.Selector) && len(svcPortByNameOrNumber(svc, portNameOrNumber)) > 0 {
			matching = append(matching, svc)
		}
	}
	return matching, nil
}

// FindMatchingPort finds the matching container associated with portNameOrNumber
// in the given service.
func FindMatchingPort(obj kates.Object, portNameOrNumber string, svc *kates.Service) (
	sPort *kates.ServicePort,
	cn *kates.Container,
	cPortIndex int,
	err error,
) {
	podTemplate, err := GetPodTemplateFromObject(obj)
	if err != nil {
		return nil, nil, 0, err
	}

	cns := podTemplate.Spec.Containers
	// For now, we only support intercepting one port on a given service.
	ports := svcPortByNameOrNumber(svc, portNameOrNumber)
	switch numPorts := len(ports); {
	case numPorts == 0:
		// this may happen when portNameOrNumber is specified but none of the
		// ports match
		return nil, nil, 0, ObjErrorf(obj, "found no Service with a port that matches any container in this workload")

	case numPorts > 1:
		return nil, nil, 0, ObjErrorf(obj, `found matching Service with multiple matching ports.
Please specify the Service port you want to intercept by passing the --port=local:svcPortName flag.`)
	default:
	}
	port := ports[0]
	var matchingServicePort *corev1.ServicePort
	var matchingContainer *corev1.Container
	var containerPortIndex int

	if port.TargetPort.Type == intstr.String {
		portName := port.TargetPort.StrVal
		for ci := 0; ci < len(cns) && matchingContainer == nil; ci++ {
			cn := &cns[ci]
			for pi := range cn.Ports {
				if cn.Ports[pi].Name == portName {
					matchingServicePort = port
					matchingContainer = cn
					containerPortIndex = pi
					break
				}
			}
		}
	} else {
		portNum := port.TargetPort.IntVal
		// Here we are using containerPortIndex <=0 instead of matchingContainer == nil because if a
		// container has no ports, we want to use it but we don't want
		// to break out of the loop looking at containers in case there
		// is a better fit.  Currently, that is a container where the
		// ContainerPort matches the targetPort in the service.
		for ci := 0; ci < len(cns) && containerPortIndex <= 0; ci++ {
			cn := &cns[ci]
			if len(cn.Ports) == 0 {
				matchingServicePort = port
				matchingContainer = cn
				containerPortIndex = -1
			}
			for pi := range cn.Ports {
				if cn.Ports[pi].ContainerPort == portNum {
					matchingServicePort = port
					matchingContainer = cn
					containerPortIndex = pi
					break
				}
			}
		}
	}

	if matchingServicePort == nil {
		return nil, nil, 0, ObjErrorf(obj, "found no Service with a port that matches any container in this workload")
	}
	return matchingServicePort, matchingContainer, containerPortIndex, nil
}
