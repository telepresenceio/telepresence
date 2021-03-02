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
	localIP net.IP
	dev     *ppf.Handle
	token   string
}

var _ FirewallRouter = (*pfRouter)(nil)

func newRouter(name string, localIP net.IP) *pfRouter {
	return &pfRouter{
		routingTableCommon: routingTableCommon{
			Name:     name,
			mappings: make(map[Destination]*Route),
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

func (t *pfRouter) hasRuleForIP(ip net.IP) bool {
	for k := range t.mappings {
		if k.IP().Equal(ip) {
			return true
		}
	}
	return false
}

func (e *Route) less(o *Route) bool {
	return e.Destination < o.Destination || e.Destination == o.Destination && e.ToPort < o.ToPort
}

func (t *pfRouter) sorted() []*Route {
	routes := make([]*Route, len(t.mappings))

	index := 0
	for _, v := range t.mappings {
		routes[index] = v
		index++
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].less(routes[j]) })
	return routes
}

// rules writes the current port forward mapping rules to the given writer
func (t *pfRouter) rules() (string, error) {
	var err error
	w := &strings.Builder{}
	rules := t.sorted()
	for _, r := range rules {
		ports := r.Ports()
		if len(ports) == 0 {
			if _, err = fmt.Fprintf(w, "rdr pass on lo0 inet proto %s to %s -> 127.0.0.1 port %d\n", r.Proto(), r.IP(), r.ToPort); err != nil {
				return "", err
			}
			continue
		}
		for _, port := range ports {
			_, err = fmt.Fprintf(w, "rdr pass on lo0 inet proto %s to %s port %d -> 127.0.0.1 port %d\n", r.Proto(), r.IP(), port, r.ToPort)
			if err != nil {
				return "", err
			}
		}
	}
	if _, err = fmt.Fprintln(w, "pass out quick inet proto tcp to 127.0.0.1/32"); err != nil {
		return "", err
	}
	for _, r := range rules {
		ports := r.Ports()
		if len(ports) == 0 {
			if _, err = fmt.Fprintf(w, "pass out route-to lo0 inet proto %s to %s keep state\n", r.Proto(), r.IP()); err != nil {
				return "", err
			}
			continue
		}
		for _, port := range ports {
			if _, err = fmt.Fprintf(w, "pass out route-to lo0 inet proto %s to %s port %d keep state\n", r.Proto(), r.IP(), port); err != nil {
				return "", err
			}
		}
	}
	return w.String(), nil
}

func (t *pfRouter) Flush(ctx context.Context) error {
	rules, err := t.rules()
	if err != nil {
		return err
	}
	return pf(ctx, []string{"-a", t.Name, "-f", "/dev/stdin"}, rules)
}

var actions = []ppf.Action{ppf.ActionPass, ppf.ActionRDR}

func (t *pfRouter) Enable(c context.Context) error {
	var err error
	if t.dev, err = ppf.Open(); err != nil {
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

	routesChanged := false
	for _, v := range t.mappings {
		changed, err := t.Add(c, v)
		if err != nil {
			return err
		}
		if changed {
			routesChanged = true
		}
	}
	if routesChanged {
		if err = t.Flush(c); err != nil {
			return err
		}
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

	for _, action := range actions {
	OUTER:
		for {
			rules, err := t.dev.Rules(action)
			if err != nil {
				dlog.Error(c, err)
				continue
			}

			for _, rule := range rules {
				if rule.AnchorCall() == t.Name {
					dlog.Debugf(c, "removing rule: %v", rule)
					if err = t.dev.RemoveRule(rule); err != nil {
						dlog.Error(c, err)
					}
					continue OUTER
				}
			}
			break
		}
	}
	return nil
}

func (t *pfRouter) Add(c context.Context, route *Route) (bool, error) {
	if old, ok := t.mappings[route.Destination]; ok {
		if route.ToPort == old.ToPort {
			return false, nil
		}
		if _, err := t.Clear(c, route); err != nil {
			return false, err
		}
	}
	t.mappings[route.Destination] = route
	// Add an entry to the routing table to make sure the firewall-to-socks worker's response
	// packets get written to the correct interface.
	if err := dexec.CommandContext(c, "route", "add", route.IP().String()+"/32", t.localIP.String()).Run(); err != nil {
		return false, err
	}
	return true, nil
}

func (t *pfRouter) Clear(ctx context.Context, route *Route) (bool, error) {
	if _, ok := t.mappings[route.Destination]; ok {
		delete(t.mappings, route.Destination)
		if !t.hasRuleForIP(route.IP()) {
			if err := dexec.CommandContext(ctx, "route", "delete", route.IP().String()+"/32", t.localIP.String()).Run(); err != nil {
				return false, err
			}
		}
		return true, nil
	}
	return false, nil
}

func (t *pfRouter) GetOriginalDst(conn *net.TCPConn) (host string, err error) {
	remote := conn.RemoteAddr().(*net.TCPAddr)
	local := conn.LocalAddr().(*net.TCPAddr)
	addr, port, err := t.dev.NatLook(remote.IP.String(), remote.Port, local.IP.String(), local.Port)
	if err != nil {
		return
	}

	return fmt.Sprintf("%s:%d", addr, port), nil
}
