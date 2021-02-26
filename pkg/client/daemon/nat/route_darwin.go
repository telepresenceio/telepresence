package nat

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
	ppf "github.com/datawire/pf"
)

type pfRouter struct {
	routingTableCommon
	localIP  net.IP
	dev      *ppf.Handle
	token    string
	curRules string
}

var _ FirewallRouter = (*pfRouter)(nil)

func newRouter(name string, localIP net.IP) *pfRouter {
	return &pfRouter{
		routingTableCommon: routingTableCommon{
			Name:     name,
			Mappings: make(map[Address]string),
		},
		localIP: localIP,
	}
}

type withoutCancel struct {
	context.Context
}

func (withoutCancel) Deadline() (deadline time.Time, ok bool) { return }
func (withoutCancel) Done() <-chan struct{}                   { return nil }
func (withoutCancel) Err() error                              { return nil }
func (c withoutCancel) String() string                        { return fmt.Sprintf("%v.WithoutCancel", c.Context) }

func pf(ctx context.Context, args []string, stdin string) error {
	// We specifically don't want to use the cancellation of 'ctx' for pfctl, because
	// interrupting pfctl may result in instabilities in macOS packet filtering.  But we still
	// want to use dexec instead of os/exec because we want dexec's logging.
	cmd := dexec.CommandContext(withoutCancel{ctx}, "pfctl", args...)
	cmd.Stdin = strings.NewReader(stdin)
	return cmd.Run()
}

func pfo(ctx context.Context, args ...string) ([]byte, error) {
	// We specifically don't want to use the cancellation of 'ctx' for pfctl, because
	// interrupting pfctl may result in instabilities in macOS packet filtering.  But we still
	// want to use dexec instead of os/exec because we want dexec's logging.
	return dexec.CommandContext(withoutCancel{ctx}, "pfctl", args...).CombinedOutput()
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
		result = append(result, fmt.Sprintf("proto %s to %s", a.Proto, a.IP))
	} else {
		for _, port := range ports {
			addr := fmt.Sprintf("proto %s to %s", a.Proto, a.IP)
			if port != "" {
				addr += fmt.Sprintf(" port %s", port)
			}

			result = append(result, addr)
		}
	}

	return
}

func (t *pfRouter) hasRuleForIP(ip string) bool {
	for addr := range t.Mappings {
		if addr.IP == ip {
			return true
		}
	}
	return false
}

func (t *pfRouter) sorted() []Entry {
	entries := make([]Entry, len(t.Mappings))

	index := 0
	for k, v := range t.Mappings {
		entries[index] = Entry{k, v}
		index++
	}

	sort.Slice(entries, func(i, j int) bool {
		return strings.Compare(entries[i].String(), entries[j].String()) < 0
	})

	return entries
}

func (t *pfRouter) rules() (rulesStr string, changed bool) {
	var result string
	if t.dev != nil {
		entries := t.sorted()

		for _, entry := range entries {
			dst := entry.Destination
			for _, addr := range fmtDest(dst) {
				result += "rdr pass on lo0 inet " + addr + " -> 127.0.0.1 port " + entry.Port + "\n"
			}
		}

		result += "pass out quick inet proto tcp to 127.0.0.1/32\n"

		for _, entry := range entries {
			dst := entry.Destination
			for _, addr := range fmtDest(dst) {
				result += "pass out route-to lo0 inet " + addr + " keep state\n"
			}
		}
	}
	changed = result != t.curRules
	if changed {
		t.curRules = result
	}
	return result, changed
}

func (t *pfRouter) updateAnchor(ctx context.Context) error {
	rules, changed := t.rules()
	if !changed {
		return nil
	}
	return pf(ctx, []string{"-a", t.Name, "-f", "/dev/stdin"}, rules)
}

var actions = []ppf.Action{ppf.ActionPass, ppf.ActionRDR}

func (t *pfRouter) Enable(c context.Context) error {
	var err error
	t.dev, err = ppf.Open()
	if err != nil {
		return err
	}

	for _, action := range actions {
		var rule ppf.Rule
		err = rule.SetAnchorCall(t.Name)
		if err != nil {
			return err
		}
		rule.SetAction(action)
		rule.SetQuick(true)
		err = t.dev.PrependRule(rule)
		if err != nil {
			return err
		}
	}

	_ = pf(c, []string{"-a", t.Name, "-F", "all"}, "")

	// XXX: blah, this generates a syntax error, but also appears
	// necessary to make anything work. I'm guessing there is some
	// sort of side effect, like it is clearing rules or
	// something, although notably loading an empty ruleset
	// doesn't seem to work, it has to be a syntax error of some
	// kind.
	_ = pf(c, []string{"-f", "/dev/stdin"}, "pass on lo0")

	_ = t.updateAnchor(c)
	for k, v := range t.Mappings {
		_ = t.forward(c, k.Proto, k.IP, k.Port, v)
	}

	output, err := pfo(c, "-E")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(output), "\n") {
		parts := strings.Split(line, ":")
		if len(parts) == 2 && strings.TrimSpace(parts[0]) == "Token" {
			t.token = strings.TrimSpace(parts[1])
			break
		}
	}

	if t.token == "" {
		return errors.New("unable to parse token")
	}
	return nil
}

func (t *pfRouter) Disable(c context.Context) error {
	defer func() {
		_ = pf(c, []string{"-a", t.Name, "-F", "all"}, "")
	}()
	_ = pf(c, []string{"-X", t.token}, "")

	if t.dev != nil {
		for _, action := range actions {
		OUTER:
			for {
				rules, err := t.dev.Rules(action)
				if err != nil {
					return err
				}

				for _, rule := range rules {
					if rule.AnchorCall() == t.Name {
						dlog.Debugf(c, "removing rule: %v", rule)
						err = t.dev.RemoveRule(rule)
						if err != nil {
							return err
						}
						continue OUTER
					}
				}
				break
			}
		}
	}
	return nil
}

func (t *pfRouter) ForwardTCP(c context.Context, ips []string, port, toPort string) error {
	for _, ip := range ips {
		if err := t.forward(c, "tcp", ip, port, toPort); err != nil {
			dlog.Errorf(c, "forward tcp")
		}
	}
	return nil
}

func (t *pfRouter) ForwardUDP(c context.Context, ips []string, port, toPort string) error {
	for _, ip := range ips {
		if err := t.forward(c, "udp", ip, port, toPort); err != nil {
			dlog.Errorf(c, "forward udp")
		}
	}
	return nil
}

func (t *pfRouter) forward(c context.Context, protocol, ip, port, toPort string) error {
	if err := t.clear(c, protocol, ip, port); err != nil {
		return err
	}
	t.Mappings[Address{protocol, ip, port}] = toPort
	// Add an entry to the routing table to make sure the firewall-to-socks worker's response
	// packets get written to the correct interface.
	if err := dexec.CommandContext(c, "route", "add", ip+"/32", t.localIP.String()).Run(); err != nil {
		return err
	}
	if err := t.updateAnchor(c); err != nil {
		return err
	}
	return nil
}

func (t *pfRouter) ClearTCP(c context.Context, ips []string, port string) error {
	for _, ip := range ips {
		if err := t.clear(c, "tcp", ip, port); err != nil {
			dlog.Errorf(c, "clear tcp")
		}
	}
	if err := t.updateAnchor(c); err != nil {
		return err
	}
	return nil
}

func (t *pfRouter) ClearUDP(c context.Context, ips []string, port string) error {
	for _, ip := range ips {
		if err := t.clear(c, "udp", ip, port); err != nil {
			dlog.Errorf(c, "clear udp")
		}
	}
	if err := t.updateAnchor(c); err != nil {
		return err
	}
	return nil
}

func (t *pfRouter) clear(ctx context.Context, protocol, ip, port string) error {
	delete(t.Mappings, Address{protocol, ip, port})
	if !t.hasRuleForIP(ip) {
		if err := dexec.CommandContext(ctx, "route", "delete", ip+"/32", t.localIP.String()).Run(); err != nil {
			return err
		}
	}
	return nil
}

func (t *pfRouter) GetOriginalDst(conn *net.TCPConn) (rawaddr []byte, host string, err error) {
	remote := conn.RemoteAddr().(*net.TCPAddr)
	local := conn.LocalAddr().(*net.TCPAddr)
	addr, port, err := t.dev.NatLook(remote.IP.String(), remote.Port, local.IP.String(), local.Port)
	if err != nil {
		return
	}

	return nil, fmt.Sprintf("%s:%d", addr, port), nil
}
