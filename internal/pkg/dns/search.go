package dns

import (
	"runtime"
	"strings"

	"github.com/datawire/teleproxy/pkg/tpu"
)

type searchDomains struct {
	Interface string
	Domains   string
}

func OverrideSearchDomains(domains string) func() {
	if runtime.GOOS != "darwin" {
		return func() {}
	}

	ifaces, _ := getIfaces()
	previous := []searchDomains{}

	for _, iface := range ifaces {
		// setup dns search path
		domain, _ := getSearchDomains(iface)
		setSearchDomains(iface, domains)
		previous = append(previous, searchDomains{iface, domain})
	}

	// return function to restore dns search paths
	return func() {
		for _, prev := range previous {
			setSearchDomains(prev.Interface, prev.Domains)
		}
	}
}

func getIfaces() (ifaces []string, err error) {
	lines, err := tpu.CmdLogf([]string{"networksetup", "-listallnetworkservices"}, log)
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

func getSearchDomains(iface string) (domains string, err error) {
	domains, err = tpu.CmdLogf([]string{"networksetup", "-getsearchdomains", iface}, log)
	domains = strings.TrimSpace(domains)
	return
}

func setSearchDomains(iface, domains string) (err error) {
	_, err = tpu.CmdLogf([]string{"networksetup", "-setsearchdomains", iface, domains}, log)
	return
}
