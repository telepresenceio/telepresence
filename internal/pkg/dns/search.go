package dns

import (
	"fmt"
	"log"
	"os/exec"
	"runtime"
	"strings"
)

type searchDomains struct {
	Interface string
	Domains string
}

func OverrideSearchDomains(domains string) func() {
	if runtime.GOOS != "darwin" { return func() {} }

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
	lines, err := shell("networksetup -listallnetworkservices | fgrep -v '*'")
	if err != nil { return }
	for _, line := range strings.Split(lines, "\n") {
		line = strings.TrimSpace(line)
		if len(line) > 0 {
			ifaces = append(ifaces, line)
		}
	}
	return
}

func getSearchDomains(iface string) (domains string, err error) {
	domains, err = shell(fmt.Sprintf("networksetup -getsearchdomains '%s'", iface))
	domains = strings.TrimSpace(domains)
	return
}

func setSearchDomains(iface, domains string) (err error) {
	_, err = shell(fmt.Sprintf("networksetup -setsearchdomains '%s' '%s'", iface, domains))
	return
}

func shell(command string) (result string, err error) {
	log.Println(command)
	cmd := exec.Command("sh", "-c", command)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Println(err)
		return
	}
	log.Printf("%s", out)
	result = string(out)
	return
}
