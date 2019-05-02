// +build darwin

package nat

import (
	"fmt"
	"net"
	"strings"

	ppf "github.com/datawire/pf"

	"github.com/datawire/teleproxy/pkg/supervisor"
)

type Translator struct {
	commonTranslator
	dev *ppf.Handle
}

func pf(p *supervisor.Process, args []string, stdin string) {
	cmd := p.Command("pfctl", args...)
	cmd.Stdin = strings.NewReader(stdin)
	err := cmd.Start()
	if err != nil {
		panic(err)
	}
	cmd.Wait()
}

func (t *Translator) rules() string {
	if t.dev == nil {
		return ""
	}

	entries := t.sorted()

	result := ""
	for _, entry := range entries {
		dst := entry.Destination
		result += ("rdr pass on lo0 inet proto " + dst.Proto + " to " + dst.Ip + " -> 127.0.0.1 port " +
			entry.Port + "\n")
	}

	result += "pass out quick inet proto tcp to 127.0.0.1/32\n"

	for _, entry := range entries {
		dst := entry.Destination
		result += "pass out route-to lo0 inet proto " + dst.Proto + " to " + dst.Ip + " keep state\n"
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

	pf(p, []string{"-f", "/dev/stdin"}, "pass on lo0")
	pf(p, []string{"-a", t.Name, "-f", "/dev/stdin"}, t.rules())

	t.dev.Start()
}

func (t *Translator) Disable(p *supervisor.Process) {
	if t.dev != nil {
		t.dev.Stop()

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

func (t *Translator) ForwardTCP(p *supervisor.Process, ip, toPort string) {
	t.forward(p, "tcp", ip, toPort)
}

func (t *Translator) ForwardUDP(p *supervisor.Process, ip, toPort string) {
	t.forward(p, "udp", ip, toPort)
}

func (t *Translator) forward(p *supervisor.Process, protocol, ip, toPort string) {
	t.clear(protocol, ip)
	t.Mappings[Address{protocol, ip}] = toPort
	pf(p, []string{"-a", t.Name, "-f", "/dev/stdin"}, t.rules())
}

func (t *Translator) ClearTCP(p *supervisor.Process, ip string) {
	t.clear("tcp", ip)
	pf(p, []string{"-a", t.Name, "-f", "/dev/stdin"}, t.rules())
}

func (t *Translator) ClearUDP(p *supervisor.Process, ip string) {
	t.clear("udp", ip)
	pf(p, []string{"-a", t.Name, "-f", "/dev/stdin"}, t.rules())
}

func (t *Translator) clear(protocol, ip string) {
	delete(t.Mappings, Address{protocol, ip})
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
