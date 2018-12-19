// +build darwin

package nat

import (
	"fmt"
	ppf "github.com/datawire/pf"
	"log"
	"net"
	"os/exec"
	"strings"
)

type Translator struct {
	commonTranslator
	dev *ppf.Handle
}

func pf(args []string, stdin string) (err error) {
	cmd := exec.Command("pfctl", args...)
	cmd.Stdin = strings.NewReader(stdin)
	log.Printf("pfctl %s < %s\n", strings.Join(args, " "), stdin)
	out, err := cmd.CombinedOutput()
	if len(out) > 0 {
		log.Printf("%s", out)
	}
	if err != nil {
		log.Println(err)
		return fmt.Errorf("IN:%s\nOUT:%s\nERR:%s\n", strings.TrimSpace(stdin), strings.TrimSpace(string(out)),
			err)
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

func (t *Translator) Enable() {
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

	pf([]string{"-a", t.Name, "-F", "all"}, "")

	pf([]string{"-f", "/dev/stdin"}, "pass on lo0")
	pf([]string{"-a", t.Name, "-f", "/dev/stdin"}, t.rules())

	t.dev.Start()
}

func (t *Translator) Disable() {
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
						log.Printf("Removing rule: %v\n", rule)
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

	pf([]string{"-a", t.Name, "-F", "all"}, "")
}

func (t *Translator) ForwardTCP(ip, toPort string) {
	t.forward("tcp", ip, toPort)
}

func (t *Translator) ForwardUDP(ip, toPort string) {
	t.forward("udp", ip, toPort)
}

func (t *Translator) forward(protocol, ip, toPort string) {
	t.clear(protocol, ip)
	t.Mappings[Address{protocol, ip}] = toPort
	pf([]string{"-a", t.Name, "-f", "/dev/stdin"}, t.rules())
}

func (t *Translator) ClearTCP(ip string) {
	t.clear("tcp", ip)
	pf([]string{"-a", t.Name, "-f", "/dev/stdin"}, t.rules())
}

func (t *Translator) ClearUDP(ip string) {
	t.clear("udp", ip)
	pf([]string{"-a", t.Name, "-f", "/dev/stdin"}, t.rules())
}

func (t *Translator) clear(protocol, ip string) {
	if _, exists := t.Mappings[Address{protocol, ip}]; exists {
		delete(t.Mappings, Address{protocol, ip})
	}
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
