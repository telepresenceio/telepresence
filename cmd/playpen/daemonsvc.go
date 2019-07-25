package main

import (
	"crypto/tls"
	"fmt"
	"io/ioutil"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/datawire/teleproxy/pkg/supervisor"
	rpc "github.com/gorilla/rpc/v2"
	"github.com/gorilla/rpc/v2/json2"
	"github.com/pkg/errors"
)

var teleproxy = ""

// FindTeleproxy finds a compatible version of Teleproxy in your PATH and saves
// it in a global
func FindTeleproxy() error {
	if len(teleproxy) == 0 {
		path, err := exec.LookPath("teleproxy")
		if err != nil {
			return err
		}
		cmd := exec.Command(path, "--version")
		outputBytes, err := cmd.CombinedOutput()
		if err != nil {
			return errors.Wrap(err, "teleproxy --version")
		}
		output := string(outputBytes)
		if !strings.Contains(output, "version 0.6") {
			return fmt.Errorf(
				"required teleproxy 0.6.x not found; found %s in your PATH",
				output,
			)
		}
		teleproxy = path
	}
	return nil
}

// TrimRightSpace returns a slice of the string s, with all trailing white space
// removed, as defined by Unicode.
func TrimRightSpace(s string) string {
	return strings.TrimRightFunc(s, unicode.IsSpace)
}

// getRPCServer returns an RPC server that locks around every call
func getRPCServer(p *supervisor.Process) *rpc.Server {
	serverLock := new(sync.Mutex)

	rpcServer := rpc.NewServer()
	rpcServer.RegisterCodec(json2.NewCodec(), "application/json")
	rpcServer.RegisterBeforeFunc(func(i *rpc.RequestInfo) {
		p.Logf("RPC call START method=%s", i.Method)
		serverLock.Lock()
	})
	rpcServer.RegisterAfterFunc(func(i *rpc.RequestInfo) {
		p.Logf("RPC call END method=%s err=%v", i.Method, i.Error)
		serverLock.Unlock()
	})
	return rpcServer
}

// checkNetOverride checks the status of teleproxy intercept by doing the
// equivalent of curl http://teleproxy/api/tables/. It's okay to create a new
// client each time because we don't want to reuse connections.
func checkNetOverride() error {
	client := http.Client{Timeout: 3 * time.Second}
	res, err := client.Get(fmt.Sprintf(
		"http://teleproxy%d.cachebust.telepresence.io/api/tables",
		time.Now().Unix(),
	))
	if err != nil {
		return err
	}
	_, err = ioutil.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		return err
	}
	return nil
}

// checkBridge checks the status of teleproxy bridge by doing the equivalent of
// curl -k https://kubernetes/api/. It's okay to create a new client each time
// because we don't want to reuse connections.
func checkBridge() error {
	// A zero-value transport is (probably) okay because we set a tight overall
	// timeout on the client
	tr := &http.Transport{
		// #nosec G402
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := http.Client{Timeout: 3 * time.Second, Transport: tr}
	res, err := client.Get("https://kubernetes.default/api/")
	if err != nil {
		return err
	}
	_, err = ioutil.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		return err
	}
	return nil
}

// DaemonService is the RPC Service used to export Playpen Daemon functionality
// to the client
type DaemonService struct {
	p              *supervisor.Process
	network        Resource
	cluster        *KCluster
	bridge         Resource
	intercepts     []*InterceptInfo
	interceptables []string
}

// MakeDaemonService creates a DaemonService object
func MakeDaemonService(p *supervisor.Process) (*DaemonService, error) {
	if err := FindTeleproxy(); err != nil {
		return nil, err
	}
	netOverride, err := CheckedRetryingCommand(
		p,
		"netOverride",
		[]string{teleproxy, "--mode", "intercept"},
		&RunAsInfo{},
		checkNetOverride,
		10*time.Second,
	)
	if err != nil {
		return nil, errors.Wrap(err, "teleproxy initial launch")
	}
	return &DaemonService{
		p:       p,
		network: netOverride,
	}, nil
}

// Status reports the current status of the daemon
func (d *DaemonService) Status(_ *http.Request, _ *EmptyArgs, reply *StringReply) error {
	res := new(strings.Builder)
	defer func() { reply.Message = TrimRightSpace(res.String()) }()

	if !d.network.IsOkay() {
		fmt.Fprintln(res, "Network overrides NOT established")
	}
	if d.cluster == nil {
		fmt.Fprintln(res, "Not connected")
		return nil
	}
	if d.cluster.IsOkay() {
		fmt.Fprintln(res, "Connected")
	} else {
		fmt.Fprintln(res, "Attempting to reconnect...")
	}
	fmt.Fprintf(res, "  Context:       %s (%s)\n", d.cluster.Context(), d.cluster.Server())
	if d.bridge != nil && d.bridge.IsOkay() {
		fmt.Fprintln(res, "  Proxy:         ON (networking to the cluster is enabled)")
	} else {
		fmt.Fprintln(res, "  Proxy:         OFF (attempting to connect...)")
	}
	fmt.Fprintf(res, "  Interceptable: %d deployments\n", len(d.interceptables))
	fmt.Fprintf(res, "  Intercepts:    ? total, %d local\n", len(d.intercepts))
	return nil
}

// Connect the daemon to a cluster
func (d *DaemonService) Connect(_ *http.Request, args *ConnectArgs, reply *StringReply) error {
	// Sanity checks
	if d.cluster != nil {
		reply.Message = "Already connected"
		return nil
	}
	if d.bridge != nil {
		reply.Message = "Not ready: Trying to disconnect"
		return nil
	}
	if !d.network.IsOkay() {
		reply.Message = "Not ready: Establishing network overrides"
		return nil
	}

	cluster, err := TrackKCluster(d.p, args)
	if err != nil {
		reply.Message = err.Error()
		return nil
	}
	d.cluster = cluster

	bridge, err := CheckedRetryingCommand(
		d.p,
		"bridge",
		[]string{teleproxy, "--mode", "bridge"},
		args.RAI,
		checkBridge,
		10*time.Second,
	)
	if err != nil {
		reply.Message = err.Error()
		d.cluster.Close()
		d.cluster = nil
		return nil
	}
	d.bridge = bridge
	d.cluster.SetBridgeCheck(d.bridge.IsOkay)

	reply.Message = fmt.Sprintf(
		"Connected to context %s (%s)", d.cluster.Context(), d.cluster.Server(),
	)
	return nil
}

// Disconnect from the connected cluster
func (d *DaemonService) Disconnect(_ *http.Request, _ *EmptyArgs, reply *StringReply) error {
	// Sanity checks
	if d.cluster == nil {
		reply.Message = "Not connected"
		return nil
	}

	if d.bridge != nil {
		_ = d.bridge.Close()
		d.bridge = nil
	}
	err := d.cluster.Close()
	d.cluster = nil

	reply.Message = "Disconnected"
	return err
}

// ListIntercepts lists active intercepts
func (d *DaemonService) ListIntercepts(_ *http.Request, _ *EmptyArgs, reply *StringReply) error {
	res := &strings.Builder{}
	for idx, intercept := range d.intercepts {
		fmt.Fprintf(res, "%4d. %s\n", idx, intercept.Name)
		fmt.Fprintf(res, "      Intercepting requests to %s when\n", intercept.Deployment)
		for k, v := range intercept.Patterns {
			fmt.Fprintf(res, "      - %s: %s\n", k, v)
		}
		fmt.Fprintf(res, "      and redirecting them to %s:%d\n", intercept.TargetHost, intercept.TargetPort)
	}
	if len(d.intercepts) == 0 {
		fmt.Fprintln(res, "No intercepts")
	}
	reply.Message = res.String()
	return nil
}

// AddIntercept adds one intercept
func (d *DaemonService) AddIntercept(_ *http.Request, intercept *InterceptInfo, reply *StringReply) error {
	for _, cept := range d.intercepts {
		if cept.Name == intercept.Name {
			return errors.Errorf("Intercept with name %q already exists", intercept.Name)
		}
	}
	d.intercepts = append(d.intercepts, intercept)
	reply.Message = fmt.Sprintf("Added intercept %q", intercept.Name)
	return nil
}

// RemoveIntercept removes one intercept by name
func (d *DaemonService) RemoveIntercept(_ *http.Request, request *StringArgs, reply *StringReply) error {
	name := request.Value
	for idx, intercept := range d.intercepts {
		if intercept.Name == name {
			d.intercepts = append(d.intercepts[:idx], d.intercepts[idx+1:]...)
			reply.Message = fmt.Sprintf("Removed intercept %q", name)
			return nil
		}
	}
	return errors.Errorf("Intercept named %q not found", name)
}
