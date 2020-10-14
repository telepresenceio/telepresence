// +build darwin

package nat

import (
	"fmt"
	"net"
	"strings"

	ppf "github.com/datawire/pf"

	"github.com/datawire/ambassador/pkg/supervisor"
)

type Translator struct {
	commonTranslator
	dev   *ppf.Handle
	token string
}

func pf(p *supervisor.Process, args []string, stdin string) error {
	cmd := p.Command("pfctl", args...)
	cmd.Stdin = strings.NewReader(stdin)
	err := cmd.Start()
	if err != nil {
		panic(err)
	}
	return cmd.Wait()
}

func splitPorts(portspec string) (result []string) {
	for _, part := range strings.Split(portspec, ",") {
		result = append(result, strings.TrimSpace(part))
	}
	return
}

func fmtDest(a Address) (result []string) {
	ports := splitPorts(a.Port)

	if len(ports) == 0 {
		result = append(result, fmt.Sprintf("proto %s to %s", a.Proto, a.Ip))
	} else {
		for _, port := range ports {
			addr := fmt.Sprintf("proto %s to %s", a.Proto, a.Ip)
			if port != "" {
				addr += fmt.Sprintf(" port %s", port)
			}

			result = append(result, addr)
		}
	}

	return
}

func (t *Translator) rules() string {
	if t.dev == nil {
		return ""
	}

	entries := t.sorted()

	result := ""
	for _, entry := range entries {
		dst := entry.Destination
		for _, addr := range fmtDest(dst) {
			result += ("rdr pass on lo0 inet " + addr + " -> 127.0.0.1 port " + entry.Port + "\n")
		}
	}

	result += "pass out quick inet proto tcp to 127.0.0.1/32\n"

	for _, entry := range entries {
		dst := entry.Destination
		for _, addr := range fmtDest(dst) {
			result += "pass out route-to lo0 inet " + addr + " keep state\n"
		}
	}

	return result
}

var actions = []ppf.Action{ppf.ActionPass, ppf.ActionRDR}

func (t *Translator) Enable(p *supervisor.Process) {
	var err error
	t.dev, err = ppf.Open()
	if err != nil {
		panic(err)
	}

	for _, action := range actions {
		var rule ppf.Rule
		err = rule.SetAnchorCall(t.Name)
		if err != nil {
			panic(err)
		}
		rule.SetAction(action)
		rule.SetQuick(true)
		err = t.dev.PrependRule(rule)
		if err != nil {
			panic(err)
		}
	}

	pf(p, []string{"-a", t.Name, "-F", "all"}, "")

	// XXX: blah, this generates a syntax error, but also appears
	// necessary to make anything work. I'm guessing there is some
	// sort of side effect, like it is clearing rules or
	// something, although notably loading an empty ruleset
	// doesn't seem to work, it has to be a syntax error of some
	// kind.
	pf(p, []string{"-f", "/dev/stdin"}, "pass on lo0")
	pf(p, []string{"-a", t.Name, "-f", "/dev/stdin"}, t.rules())

	output := p.Command("pfctl", "-E").MustCaptureErr(nil)
	for _, line := range strings.Split(output, "\n") {
		parts := strings.Split(line, ":")
		if len(parts) == 2 && strings.TrimSpace(parts[0]) == "Token" {
			t.token = strings.TrimSpace(parts[1])
			break
		}
	}

	if t.token == "" {
		panic("unable to parse token")
	}
}

func (t *Translator) Disable(p *supervisor.Process) {
	_ = p.Command("pfctl", "-X", t.token).Run()

	if t.dev != nil {
		for _, action := range actions {
		OUTER:
			for {
				rules, err := t.dev.Rules(action)
				if err != nil {
					panic(err)
				}

				for _, rule := range rules {
					if rule.AnchorCall() == t.Name {
						p.Logf("removing rule: %v\n", rule)
						err = t.dev.RemoveRule(rule)
						if err != nil {
							panic(err)
						}
						continue OUTER
					}
				}
				break
			}
		}
	}

	pf(p, []string{"-a", t.Name, "-F", "all"}, "")
}

func (t *Translator) ForwardTCP(p *supervisor.Process, ip, port, toPort string) {
	t.forward(p, "tcp", ip, port, toPort)
}

func (t *Translator) ForwardUDP(p *supervisor.Process, ip, port, toPort string) {
	t.forward(p, "udp", ip, port, toPort)
}

func (t *Translator) forward(p *supervisor.Process, protocol, ip, port, toPort string) {
	t.clear(protocol, ip, port)
	t.Mappings[Address{protocol, ip, port}] = toPort
	pf(p, []string{"-a", t.Name, "-f", "/dev/stdin"}, t.rules())
}

func (t *Translator) ClearTCP(p *supervisor.Process, ip, port string) {
	t.clear("tcp", ip, port)
	pf(p, []string{"-a", t.Name, "-f", "/dev/stdin"}, t.rules())
}

func (t *Translator) ClearUDP(p *supervisor.Process, ip, port string) {
	t.clear("udp", ip, port)
	pf(p, []string{"-a", t.Name, "-f", "/dev/stdin"}, t.rules())
}

func (t *Translator) clear(protocol, ip, port string) {
	delete(t.Mappings, Address{protocol, ip, port})
}

func (t *Translator) GetOriginalDst(conn *net.TCPConn) (rawaddr []byte, host string, err error) {
	remote := conn.RemoteAddr().(*net.TCPAddr)
	local := conn.LocalAddr().(*net.TCPAddr)
	addr, port, err := t.dev.NatLook(remote.IP.String(), remote.Port, local.IP.String(), local.Port)
	if err != nil {
		return
	}

	return nil, fmt.Sprintf("%s:%d", addr, port), nil
}
