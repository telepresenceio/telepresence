package install

import (
	"fmt"
	"strconv"
	"strings"

	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
)

// FilterServicePorts iterates through a list of ports in a service and
// only returns the ports that match the given nameOrNumber. All ports will
// be returned if nameOrNumber is equal to the empty string.
func FilterServicePorts(svc *core.Service, nameOrNumber string) ([]core.ServicePort, error) {
	ports := svc.Spec.Ports
	if nameOrNumber == "" {
		return ports, nil
	}
	svcPorts := make([]core.ServicePort, 0)
	if number, err := strconv.Atoi(nameOrNumber); err != nil {
		errs := validation.IsValidPortName(nameOrNumber)
		if len(errs) > 0 {
			return nil, fmt.Errorf(strings.Join(errs, "\n"))
		}
		for _, port := range ports {
			if port.Name == nameOrNumber {
				svcPorts = append(svcPorts, port)
			}
		}
	} else {
		for _, port := range ports {
			pn := int32(0)
			if port.TargetPort.Type == intstr.Int {
				pn = port.TargetPort.IntVal
			}
			if pn == 0 {
				pn = port.Port
			}
			if pn == int32(number) {
				svcPorts = append(svcPorts, port)
			}
		}
	}
	return svcPorts, nil
}
