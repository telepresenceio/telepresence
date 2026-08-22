package manager

import (
	"fmt"
	"strings"

	"github.com/blang/semver/v4"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func validateClient(client *rpc.ClientInfo) string {
	switch {
	case client.Name == "":
		return "name must not be empty"
	case client.Namespace == "":
		return "namespace must not be empty"
	case client.InstallId == "":
		return "install ID must not be empty"
	case client.Product == "":
		return "product must not be empty"
	case client.Version == "":
		return "version must not be empty"
	}
	sv, err := semver.Parse(strings.TrimPrefix(client.Version, "v"))
	if err != nil {
		return err.Error()
	}
	if sv.Major < 2 || sv.Major == 2 && sv.Minor < 6 {
		return "client version must be at least 2.6.0"
	}
	return ""
}

func validateMechanism(mechanism *rpc.AgentInfo_Mechanism) string {
	switch {
	case mechanism.Name == "":
		return "name must not be empty"
	case mechanism.Product == "":
		return "product must not be empty"
	case mechanism.Version == "":
		return "version must not be empty"
	}

	return ""
}

func validateAgent(agent *rpc.AgentInfo) string {
	switch {
	case agent.Name == "":
		return "name must not be empty"
	case agent.Namespace == "":
		return "namespace must not be empty"
	case agent.Product == "":
		return "product must not be empty"
	case agent.Version == "":
		return "version must not be empty"
	case len(agent.Mechanisms) == 0:
		return "mechanisms must not be empty"
	}

	for idx, mechanism := range agent.Mechanisms {
		if msg := validateMechanism(mechanism); msg != "" {
			return fmt.Sprintf("mechanism %d: %s", idx+1, msg)
		}
	}

	return ""
}

func validateIntercept(spec *rpc.InterceptSpec) string {
	switch {
	case spec.Client == "":
		return "client must not be empty"
	case spec.Agent == "":
		return "agent must not be empty"
	case spec.Namespace == "":
		return "namespace must not be empty"
	case spec.Mechanism == "":
		return "mechanism must not be empty"
	}
	if kafka := spec.GetKafka(); kafka != nil {
		if len(kafka.Key) > 0 && len(kafka.KeyPrefix) > 0 {
			return "Kafka intercept cannot contain both key and key prefix"
		}
		headers := make(map[string]struct{}, len(kafka.Headers))
		for _, header := range kafka.Headers {
			if header.GetName() == "" {
				return "Kafka header name must not be empty"
			}
			if _, ok := headers[header.Name]; ok {
				return fmt.Sprintf("duplicate Kafka header %q", header.Name)
			}
			headers[header.Name] = struct{}{}
		}
		if kafka.GetOnly() && (spec.Replace || spec.Wiretap || spec.NodeAgent || spec.NoDefaultPort || spec.PortIdentifier != "" || len(spec.PodPorts) > 0) {
			return "Kafka-only intercept cannot request network interception"
		}
	}

	return ""
}
