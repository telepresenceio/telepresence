package dns

import (
	"runtime"
	"strings"

	"github.com/datawire/ambassador/pkg/supervisor"
)

type searchDomains struct {
	Interface string
	Domains   string
}

func OverrideSearchDomains(p *supervisor.Process, domains string) func() {
	if runtime.GOOS != "darwin" {
		return func() {}
	}

	ifaces, err := getIfaces(p)
	if err != nil {
		panic(err)
	}
	previous := []searchDomains{}

	for _, iface := range ifaces {
		// setup dns search path
		domain, err := getSearchDomains(p, iface)
		if err != nil {
			log("DNS: error getting search domain for interface %v: %v", iface, err)
		} else {
			setSearchDomains(p, iface, domains)
			previous = append(previous, searchDomains{iface, domain})
		}
	}

	// return function to restore dns search paths
	return func() {
		for _, prev := range previous {
			setSearchDomains(p, prev.Interface, prev.Domains)
		}
	}
}

func getIfaces(p *supervisor.Process) (ifaces []string, err error) {
	lines, err := p.Command("networksetup", "-listallnetworkservices").Capture(nil)
	if err != nil {
		return
	}
	for _, line := range strings.Split(lines, "\n") {
		if strings.Contains(line, "*") {
			continue
		}
		line = strings.TrimSpace(line)
		if len(line) > 0 {
			ifaces = append(ifaces, line)
		}
	}
	return
}

func getSearchDomains(p *supervisor.Process, iface string) (domains string, err error) {
	domains, err = p.Command("networksetup", "-getsearchdomains", iface).Capture(nil)
	domains = strings.TrimSpace(domains)
	return
}

func setSearchDomains(p *supervisor.Process, iface, domains string) (err error) {
	err = p.Command("networksetup", "-setsearchdomains", iface, domains).Run()
	return
}
