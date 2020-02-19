package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"

	"github.com/datawire/ambassador/pkg/supervisor"
)

func (d *Daemon) interceptMessage() string {
	switch {
	case d.cluster == nil:
		return "Not connected (use 'edgectl connect' to connect to your cluster)"
	case d.trafficMgr == nil:
		return "Intercept unavailable: no traffic manager"
	case !d.trafficMgr.IsOkay():
		return "Connecting to traffic manager..."
	default:
		return ""
	}
}

// InterceptInfo tracks one intercept operation
type InterceptInfo struct {
	Name       string // Name of the intercept (user/logging)
	Namespace  string // Name in which to create the Intercept mapping
	Deployment string // Name of the deployment being intercepted
	Prefix     string // Prefix to intercept (default /)
	Patterns   map[string]string
	TargetHost string
	TargetPort int
}

// Acquire an intercept from the traffic manager
func (ii *InterceptInfo) Acquire(_ *supervisor.Process, tm *TrafficManager) (int, error) {
	reqPatterns := make([]map[string]string, 0, len(ii.Patterns))
	for header, regex := range ii.Patterns {
		pattern := map[string]string{"name": header, "regex_match": regex}
		reqPatterns = append(reqPatterns, pattern)
	}
	request := map[string]interface{}{
		"name":     ii.Name,
		"patterns": reqPatterns,
	}
	reqData, err := json.Marshal(request)
	if err != nil {
		return 0, err
	}
	result, code, err := tm.request("POST", "intercept/"+ii.Deployment, reqData)
	if err != nil {
		return 0, errors.Wrap(err, "acquire intercept")
	}
	if code == 404 {
		return 0, fmt.Errorf("deployment %q is not known to the traffic manager", ii.Deployment)
	}
	if !(200 <= code && code <= 299) {
		return 0, fmt.Errorf("acquire intercept: %s: %s", http.StatusText(code), result)
	}
	port, err := strconv.Atoi(result)
	if err != nil {
		return 0, errors.Wrapf(err, "bad port number from traffic manager: %q", result)
	}
	return port, nil
}

// Retain the given intercept. This likely needs to be called every
// five seconds or so.
func (ii *InterceptInfo) Retain(_ *supervisor.Process, tm *TrafficManager, port int) error {
	data := []byte(fmt.Sprintf("{\"port\": %d}", port))
	result, code, err := tm.request("POST", "intercept/"+ii.Deployment, data)
	if err != nil {
		return errors.Wrap(err, "retain intercept")
	}
	if !(200 <= code && code <= 299) {
		return fmt.Errorf("retain intercept: %s: %s", http.StatusText(code), result)
	}
	return nil
}

// Release the given intercept.
func (ii *InterceptInfo) Release(_ *supervisor.Process, tm *TrafficManager, port int) error {
	data := []byte(fmt.Sprintf("%d", port))
	result, code, err := tm.request("DELETE", "intercept/"+ii.Deployment, data)
	if err != nil {
		return errors.Wrap(err, "release intercept")
	}
	if !(200 <= code && code <= 299) {
		return fmt.Errorf("release intercept: %s: %s", http.StatusText(code), result)
	}
	return nil
}

// ListIntercepts lists active intercepts
func (d *Daemon) ListIntercepts(_ *supervisor.Process, out *Emitter) error {
	msg := d.interceptMessage()
	if msg != "" {
		out.Println(msg)
		out.Send("intercept", msg)
		return nil
	}
	for idx, cept := range d.intercepts {
		ii := cept.ii
		out.Printf("%4d. %s\n", idx+1, ii.Name)
		out.Send("local_intercept", ii.Name)
		key := "local_intercept." + ii.Name
		out.Printf("      Intercepting requests to %s when\n", ii.Deployment)
		out.Send(key, ii.Deployment)
		for k, v := range ii.Patterns {
			out.Printf("      - %s: %s\n", k, v)
			out.Send(key+"."+k, v)
		}
		out.Printf("      and redirecting them to %s:%d\n", ii.TargetHost, ii.TargetPort)
		out.Send(key+".host", ii.TargetHost)
		out.Send(key+".port", ii.TargetPort)
	}
	if len(d.intercepts) == 0 {
		out.Println("No intercepts")
	}
	return nil
}

// AddIntercept adds one intercept
func (d *Daemon) AddIntercept(p *supervisor.Process, out *Emitter, ii *InterceptInfo) error {
	msg := d.interceptMessage()
	if msg != "" {
		out.Println(msg)
		out.Send("intercept", msg)
		return nil
	}
	for _, cept := range d.intercepts {
		if cept.ii.Name == ii.Name {
			out.Printf("Intercept with name %q already exists\n", ii.Name)
			out.Send("failed", "intercept name exists")
			out.SendExit(1)
			return nil
		}
	}
	cept, err := MakeIntercept(p, out, d.trafficMgr, d.cluster, ii)
	if err != nil {
		out.Printf("Failed to establish intercept: %s\n", err)
		out.Send("failed", err.Error())
		out.SendExit(1)
		return nil
	}
	d.intercepts = append(d.intercepts, cept)
	out.Printf("Added intercept %q\n", ii.Name)
	return nil
}

// RemoveIntercept removes one intercept by name
func (d *Daemon) RemoveIntercept(p *supervisor.Process, out *Emitter, name string) error {
	msg := d.interceptMessage()
	for idx, cept := range d.intercepts {
		if cept.ii.Name == name {
			d.intercepts = append(d.intercepts[:idx], d.intercepts[idx+1:]...)

			out.Printf("Removed intercept %q\n", name)

			if err := cept.Close(); err != nil {
				out.Printf("Error while removing intercept: %v\n", err)
				out.Send("failed", err.Error())
				out.SendExit(1)
			}
			return nil
		}
	}
	if msg != "" {
		out.Println(msg)
		out.Send("intercept", msg)
		return nil
	}
	out.Printf("Intercept named %q not found\n", name)
	out.Send("failed", "not found")
	out.SendExit(1)
	return nil
}

// ClearIntercepts removes all intercepts
func (d *Daemon) ClearIntercepts(p *supervisor.Process) error {
	for _, cept := range d.intercepts {
		if err := cept.Close(); err != nil {
			p.Logf("Closing intercept %q: %v", cept.ii.Name, err)
		}
	}
	d.intercepts = d.intercepts[:0]
	return nil
}

// Intercept is a Resource handle that represents a live intercept
type Intercept struct {
	ii            *InterceptInfo
	tm            *TrafficManager
	cluster       *KCluster
	port          int
	crc           Resource
	mappingExists bool
	ResourceBase
}

// removeMapping drops an Intercept's mapping if needed (and possible).
func (cept *Intercept) removeMapping(p *supervisor.Process) error {
	var err error
	err = nil

	if cept.mappingExists {
		p.Logf("%v: Deleting mapping", cept.ii.Name)
		delete := cept.cluster.GetKubectlCmd(p, "delete", "mapping", fmt.Sprintf("%s-mapping", cept.ii.Name))
		delete.Stdout = os.Stdout
		delete.Stderr = os.Stderr
		err = delete.Run()
		p.Logf("%v: Deleted mapping", cept.ii.Name)
	}

	if err != nil {
		return errors.Wrap(err, "Intercept: mapping could not be deleted")
	}

	return nil
}

type mappingMetadata struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type mappingSpec struct {
	AmbassadorID []string          `json:"ambassador_id"`
	Prefix       string            `json:"prefix"`
	Service      string            `json:"service"`
	Headers      map[string]string `json:"headers"`
}

type interceptMapping struct {
	APIVersion string          `json:"apiVersion"`
	Kind       string          `json:"kind"`
	Metadata   mappingMetadata `json:"metadata"`
	Spec       mappingSpec     `json:"spec"`
}

// MakeIntercept acquires an intercept and returns a Resource handle
// for it
func MakeIntercept(p *supervisor.Process, out *Emitter, tm *TrafficManager, cluster *KCluster, ii *InterceptInfo) (*Intercept, error) {
	port, err := ii.Acquire(p, tm)
	if err != nil {
		return nil, err
	}

	cept := &Intercept{ii: ii, tm: tm, cluster: cluster, port: port}
	cept.mappingExists = false
	cept.doCheck = cept.check
	cept.doQuit = cept.quit
	cept.setup(p.Supervisor(), ii.Name)

	p.Logf("%s: Intercepting via port %v, using namespace %v", ii.Name, port, ii.Namespace)
	out.Printf("%v: Intercepting\n", ii.Name)

	mapping := interceptMapping{
		APIVersion: "getambassador.io/v2",
		Kind:       "Mapping",
		Metadata: mappingMetadata{
			Name:      fmt.Sprintf("%s-mapping", ii.Name),
			Namespace: ii.Namespace,
		},
		Spec: mappingSpec{
			AmbassadorID: []string{fmt.Sprintf("intercept-%s", ii.Deployment)},
			Prefix:       ii.Prefix,
			Service:      fmt.Sprintf("telepresence-proxy:%d", port),
			Headers:      ii.Patterns,
		},
	}

	manifest, err := json.MarshalIndent(&mapping, "", "  ")

	if err != nil {
		_ = cept.Close()
		return nil, errors.Wrap(err, "Intercept: mapping could not be constructed")
	}

	p.Logf("%s: Intercept using mapping %v", ii.Name, string(manifest))
	// out.Printf("Applying intercept mapping in namespace %v:\n%v\n", ii.Namespace, string(manifest))

	apply := cluster.GetKubectlCmd(p, "apply", "-f", "-")
	apply.Stdin = strings.NewReader(string(manifest))
	apply.Stdout = os.Stdout
	apply.Stderr = os.Stderr
	err = apply.Run()

	if err != nil {
		p.Logf("%v: Intercept could not apply mapping: %v", ii.Name, err)
		_ = cept.Close()
		return nil, errors.Wrap(err, "Intercept: kubectl apply")
	}

	cept.mappingExists = true

	sshCmd := []string{
		"ssh", "-C", "-N", "telepresence@localhost",
		"-oConnectTimeout=10", "-oExitOnForwardFailure=yes",
		"-oStrictHostKeyChecking=no", "-oUserKnownHostsFile=/dev/null",
		"-p", strconv.Itoa(tm.sshPort),
		"-R", fmt.Sprintf("%d:%s:%d", cept.port, ii.TargetHost, ii.TargetPort),
	}

	p.Logf("%s: starting SSH tunnel", ii.Name)
	out.Printf("%s: starting SSH tunnel", ii.Name)

	ssh, err := CheckedRetryingCommand(p, ii.Name+"-ssh", sshCmd, nil, nil, 5*time.Second)

	if err != nil {
		_ = cept.Close()
		return nil, err
	}

	cept.crc = ssh

	return cept, nil
}

func (cept *Intercept) check(p *supervisor.Process) error {
	return cept.ii.Retain(p, cept.tm, cept.port)
}

func (cept *Intercept) quit(p *supervisor.Process) error {
	cept.done = true

	p.Logf("cept.Quit removing %v", cept.ii.Name)

	cept.removeMapping(p)

	p.Logf("cept.Quit removed %v", cept.ii.Name)

	if cept.crc != nil {
		_ = cept.crc.Close()
	}

	p.Logf("cept.Quit releasing %v", cept.ii.Name)

	if err := cept.ii.Release(p, cept.tm, cept.port); err != nil {
		p.Log(err)
	}

	p.Logf("cept.Quit released %v", cept.ii.Name)

	return nil
}
