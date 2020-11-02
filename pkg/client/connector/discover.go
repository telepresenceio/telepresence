package connector

import (
	"github.com/datawire/ambassador/pkg/kates"
	"github.com/datawire/ambassador/pkg/supervisor"
	"github.com/pkg/errors"
)

func findTrafficManager(p *supervisor.Process, namespace string) (*kates.Service, error) {
	client, err := kates.NewClient(kates.ClientOptions{})
	if err != nil {
		return nil, err
	}
	service := &kates.Service{
		TypeMeta: kates.TypeMeta{
			Kind: "Service",
		},
		ObjectMeta: kates.ObjectMeta{
			Namespace: namespace,
			Name:      "traffic-manager"},
	}
	if err = client.Get(p.Context(), service, service); err != nil {
		return nil, err
	}
	return service, nil
}

func installTrafficManager(p *supervisor.Process, namespace string) (*kates.Service, error) {
	return nil, errors.New("install of traffic-manager is not yet implemented")
}

func ensureTrafficManager(p *supervisor.Process, namespace string) (int32, int32, error) {
	svc, err := findTrafficManager(p, namespace)
	if err != nil {
		if !kates.IsNotFound(err) {
			return 0, 0, err
		}
		if svc, err = installTrafficManager(p, namespace); err != nil {
			return 0, 0, err
		}
	}

	var sshdPort, apiPort int32
	for _, port := range svc.Spec.Ports {
		switch port.Name {
		case "sshd":
			sshdPort = port.Port
		case "api":
			apiPort = port.Port
		}
	}
	if sshdPort == 0 {
		return 0, 0, errors.New("traffic-manager has no sshd port configured")
	}
	if apiPort == 0 {
		return 0, 0, errors.New("traffic-manager has no api port configured")
	}
	return sshdPort, apiPort, nil
}
