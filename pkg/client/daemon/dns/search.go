package dns

import (
	"context"
	"runtime"
	"strings"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
)

type searchDomains struct {
	Interface string
	Domains   string
}

// OverrideSearchDomains establishes overrides for the given search domains and
// returns a function that removes the overrides. This function does nothing unless
// the host OS is "darwin".
func OverrideSearchDomains(c context.Context, domains string) func() {
	if runtime.GOOS != "darwin" {
		return func() {}
	}

	ifaces, err := getIfaces(c)
	if err != nil {
		panic(err)
	}
	var previous []searchDomains

	for _, iface := range ifaces {
		// setup dns search path
		domain, err := getSearchDomains(c, iface)
		if err != nil {
			dlog.Errorf(c, "DNS: error getting search domain for interface %v: %v", iface, err)
		} else {
			_ = setSearchDomains(c, iface, domains)
			previous = append(previous, searchDomains{iface, domain})
		}
	}

	// return function to restore dns search paths
	return func() {
		for _, prev := range previous {
			_ = setSearchDomains(c, prev.Interface, prev.Domains)
		}
	}
}

func getIfaces(c context.Context) (ifaces []string, err error) {
	lines, err := dexec.CommandContext(c, "networksetup", "-listallnetworkservices").Output()
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(lines), "\n") {
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

func getSearchDomains(c context.Context, iface string) (string, error) {
	out, err := dexec.CommandContext(c, "networksetup", "-getsearchdomains", iface).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func setSearchDomains(c context.Context, iface, domains string) error {
	return dexec.CommandContext(c, "networksetup", "-setsearchdomains", iface, domains).Run()
}
