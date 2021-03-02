package nat

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/route"
	"golang.org/x/sys/unix"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
	ppf "github.com/datawire/pf"
)

type pfRouter struct {
	routingTableCommon
	localIP net.IP
	dev     *ppf.Handle
	token   string

	// OS routing table routes to add and clear on Flush
	routesToClear []*Route
	routesToAdd   []*Route
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

func pfCmd(ctx context.Context, args []string) *dexec.Cmd {
	// We specifically don't want to use the cancellation of 'ctx' for pfctl, because
	// interrupting pfctl may result in instabilities in macOS packet filtering.  But we still
	// want to use dexec instead of os/exec because we want dexec's logging.
	return dexec.CommandContext(withoutCancel{ctx}, "pfctl", args...)
}

func pf(ctx context.Context, args ...string) error {
	return pfCmd(ctx, append([]string{"-q"}, args...)).Run()
}

func pfo(ctx context.Context, args ...string) ([]byte, error) {
	return pfCmd(ctx, args).CombinedOutput()
}

func pffs(ctx context.Context, table, stdout string) error {
	return pff(ctx, table, func(w io.Writer) error {
		_, err := io.WriteString(w, stdout)
		return err
	})
}

func pff(ctx context.Context, table string, producer func(writer io.Writer) error) error {
	args := make([]string, 0, 4)
	args = append(args, "-q")
	if table != "" {
		args = append(args, "-a", table)
	}
	args = append(args, "-f-")
	cmd := pfCmd(ctx, args)

	// Avoid logging "Use of option -f" warnings on stderr
	devNul, err := os.Open("/dev/null")
	if err != nil {
		return err
	}
	defer devNul.Close()
	cmd.Stderr = devNul

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}

	go func() {
		defer stdin.Close()
		bw := bufio.NewWriter(stdin)
		if err = producer(bw); err == nil {
			err = bw.Flush()
		}
		if err != nil {
			dlog.Error(ctx, err)
		}
	}()

	return cmd.Run()
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
func (t *pfRouter) rules(w io.Writer) (err error) {
	rules := t.sorted()
	for _, r := range rules {
		ports := r.Ports()
		if len(ports) == 0 {
			if _, err = fmt.Fprintf(w, "rdr pass on lo0 inet proto %s to %s -> 127.0.0.1 port %d\n", r.Proto(), r.IP(), r.ToPort); err != nil {
				return err
			}
			continue
		}
		for _, port := range ports {
			_, err = fmt.Fprintf(w, "rdr pass on lo0 inet proto %s to %s port %d -> 127.0.0.1 port %d\n", r.Proto(), r.IP(), port, r.ToPort)
			if err != nil {
				return err
			}
		}
	}
	if _, err = fmt.Fprintln(w, "pass out quick inet proto tcp to 127.0.0.1/32"); err != nil {
		return err
	}
	for _, r := range rules {
		ports := r.Ports()
		if len(ports) == 0 {
			if _, err = fmt.Fprintf(w, "pass out route-to lo0 inet proto %s to %s keep state\n", r.Proto(), r.IP()); err != nil {
				return err
			}
			continue
		}
		for _, port := range ports {
			if _, err = fmt.Fprintf(w, "pass out route-to lo0 inet proto %s to %s port %d keep state\n", r.Proto(), r.IP(), port); err != nil {
				return err
			}
		}
	}
	return nil
}

// Flush will messages to clear and add pending OS routes and send the rules corresponding to
// the current mappings using pf -f-
func (t *pfRouter) Flush(ctx context.Context) error {
	if len(t.routesToAdd) > 0 || len(t.routesToClear) > 0 {
		err := withRouteSocket(func(routeSocket int) error {
			seq := 0
			for _, r := range t.routesToClear {
				seq++
				if err := t.routeClear(routeSocket, seq, r); err != nil {
					return err
				}
			}
			t.routesToClear = nil
			for _, r := range t.routesToAdd {
				seq++
				if err := t.routeAdd(routeSocket, seq, r); err != nil {
					return err
				}
			}
			t.routesToAdd = nil
			return nil
		})
		if err != nil {
			return err
		}
	}
	return pff(ctx, t.Name, t.rules)
}

// withRouteSocket will open the socket to where RouteMessages should be sent
// and call the given function with that socket. The socket is closed when the
// function returns
func withRouteSocket(f func(routeSocket int) error) error {
	routeSocket, err := syscall.Socket(syscall.AF_ROUTE, syscall.SOCK_RAW, syscall.AF_UNSPEC)
	if err != nil {
		return err
	}

	// Avoid the overhead of echoing messages back to sender
	if err = syscall.SetsockoptInt(routeSocket, syscall.SOL_SOCKET, syscall.SO_USELOOPBACK, 0); err != nil {
		return err
	}
	defer syscall.Close(routeSocket)
	return f(routeSocket)
}

// toRouteAddr converts an net.IP to its corresponding route.Addr
func toRouteAddr(ip net.IP) (addr route.Addr) {
	if ip4 := ip.To4(); ip4 != nil {
		dst := route.Inet4Addr{}
		copy(dst.IP[:], ip4)
		addr = &dst
	} else {
		dst := route.Inet6Addr{}
		copy(dst.IP[:], ip)
		addr = &dst
	}
	return addr
}

func fullMask(ip net.IP) (addr route.Addr) {
	if ip4 := ip.To4(); ip4 != nil {
		dst := route.Inet4Addr{}
		for i := 0; i < 4; i++ {
			dst.IP[i] = 0xff
		}
		addr = &dst
	} else {
		dst := route.Inet6Addr{}
		for i := 0; i < 16; i++ {
			dst.IP[i] = 0xff
		}
		addr = &dst
	}
	return addr
}

func (t *pfRouter) newRouteMessage(rtm, seq int, r *Route) *route.RouteMessage {
	ip := r.IP()
	return &route.RouteMessage{
		Version: syscall.RTM_VERSION,
		ID:      uintptr(os.Getpid()),
		Seq:     seq,
		Type:    rtm,
		Flags:   syscall.RTF_UP | syscall.RTF_GATEWAY | syscall.RTF_STATIC | syscall.RTF_PRCLONING,
		Addrs: []route.Addr{
			syscall.RTAX_DST:     toRouteAddr(ip),
			syscall.RTAX_GATEWAY: toRouteAddr(t.localIP),
			syscall.RTAX_NETMASK: fullMask(ip),
		},
	}
}

func (t *pfRouter) routeAdd(routeSocket, seq int, r *Route) error {
	m := t.newRouteMessage(syscall.RTM_ADD, seq, r)
	wb, err := m.Marshal()
	if err != nil {
		return err
	}
	_, err = syscall.Write(routeSocket, wb)
	if err == unix.EEXIST {
		// route exists, that's OK
		err = nil
	}
	return err
}

func (t *pfRouter) routeClear(routeSocket, seq int, r *Route) error {
	m := t.newRouteMessage(syscall.RTM_DELETE, seq, r)
	wb, err := m.Marshal()
	if err != nil {
		return err
	}
	_, err = syscall.Write(routeSocket, wb)
	if err == unix.ESRCH {
		// route doesn't exist, that's OK
		err = nil
	}
	return err
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

	_ = pf(c, "-a", t.Name, "-F", "all")

	// XXX: blah, this generates a syntax error, but also appears
	// necessary to make anything work. I'm guessing there is some
	// sort of side effect, like it is clearing rules or
	// something, although notably loading an empty ruleset
	// doesn't seem to work, it has to be a syntax error of some
	// kind.
	_ = pffs(c, "", "pass on lo0")

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
		_ = pf(c, "-a", t.Name, "-F", "all")
	}()
	_ = pf(c, "-X", t.token)

	// remove all added routes from the OS routing table
	_ = withRouteSocket(func(routeSocket int) error {
		seq := 0
		for _, r := range t.mappings {
			seq++
			_ = t.routeClear(routeSocket, seq, r)
		}
		return nil
	})

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
	t.routesToAdd = append(t.routesToAdd, route)
	return true, nil
}

func (t *pfRouter) Clear(_ context.Context, route *Route) (bool, error) {
	if _, ok := t.mappings[route.Destination]; ok {
		delete(t.mappings, route.Destination)
		if !t.hasRuleForIP(route.IP()) {
			t.routesToClear = append(t.routesToClear, route)
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
