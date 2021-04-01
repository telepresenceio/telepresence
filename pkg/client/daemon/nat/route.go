package nat

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

type Table struct {
	Name   string
	Routes []*Route
}

type Route struct {
	Destination
	ToPort int
}

func ParsePorts(portsStr string) ([]int, error) {
	portsStr = strings.TrimSpace(portsStr)
	if len(portsStr) == 0 {
		return nil, nil
	}
	portStrs := strings.Split(portsStr, ",")
	ports := make([]int, len(portStrs))
	for i, p := range portStrs {
		ps := strings.TrimSpace(p)
		var err error
		if ports[i], err = strconv.Atoi(ps); err != nil {
			return nil, fmt.Errorf("invalid port number in route %q", ps)
		}
	}
	return ports, nil
}

func NewRoute(proto string, ip net.IP, ports []int, toPort int) (*Route, error) {
	dest, err := NewDestination(proto, ip, ports)
	if err != nil {
		return nil, err
	}

	return &Route{
		Destination: dest,
		ToPort:      toPort,
	}, nil
}

func (e *Route) Equal(o *Route) bool {
	return e == o || e != nil && o != nil && e.Destination == o.Destination && e.ToPort == o.ToPort
}

func (e *Route) String() string {
	return fmt.Sprintf("%s->%d", e.Destination, e.ToPort)
}
