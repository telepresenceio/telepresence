package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"

	"github.com/datawire/ambassador/pkg/api/edgectl/rpc"
	"github.com/datawire/ambassador/pkg/supervisor"
)

func (d *daemon) interceptStatus() (rpc.InterceptError, string) {
	ie := rpc.InterceptError_InterceptOk
	msg := ""
	switch {
	case d.cluster == nil:
		ie = rpc.InterceptError_NoConnection
	case d.trafficMgr == nil:
		ie = rpc.InterceptError_NoTrafficManager
	case !d.trafficMgr.IsOkay():
		if d.trafficMgr.apiErr != nil {
			ie = rpc.InterceptError_TrafficManagerError
			msg = d.trafficMgr.apiErr.Error()
		} else {
			ie = rpc.InterceptError_TrafficManagerConnecting
		}
	}
	return ie, msg
}

// InterceptInfo tracks one intercept operation
type InterceptInfo struct {
	*rpc.InterceptRequest
}

// path returns the URL path for this intercept
func (ii *InterceptInfo) path() string {
	return fmt.Sprintf("intercept/%s/%s", ii.Namespace, ii.Deployment)
}

// PreviewURL returns the Service Preview URL for this intercept if it is
// configured appropriately, or the empty string otherwise.
func (ii *InterceptInfo) PreviewURL(hostname string) (url string) {
	if hostname == "" || len(ii.Patterns) != 1 {
		return
	}

	for header, token := range ii.Patterns {
		if strings.ToLower(header) != "x-service-preview" {
			return
		}
		url = fmt.Sprintf("https://%s/.ambassador/service-preview/%s/", hostname, token)
	}

	return
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

	result, code, err := tm.request("POST", ii.path(), reqData)
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
	result, code, err := tm.request("POST", ii.path(), data)
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
	result, code, err := tm.request("DELETE", ii.path(), data)
	if err != nil {
		return errors.Wrap(err, "release intercept")
	}
	if !(200 <= code && code <= 299) {
		return fmt.Errorf("release intercept: %s: %s", http.StatusText(code), result)
	}
	return nil
}

// listIntercepts lists active intercepts
func (d *daemon) listIntercepts(_ *supervisor.Process) *rpc.ListInterceptsResponse {
	r := &rpc.ListInterceptsResponse{}
	r.Error, r.Text = d.interceptStatus()
	if r.Error != rpc.InterceptError_InterceptOk {
		return r
	}
	r.Intercepts = make([]*rpc.ListInterceptsResponse_ListEntry, len(d.intercepts))
	for idx, cept := range d.intercepts {
		ii := cept.ii
		r.Intercepts[idx] = &rpc.ListInterceptsResponse_ListEntry{
			Name:       ii.Name,
			Namespace:  ii.Namespace,
			Deployment: ii.Deployment,
			PreviewURL: ii.PreviewURL(d.trafficMgr.previewHost),
			Patterns:   ii.Patterns,
			TargetHost: ii.TargetHost,
			TargetPort: ii.TargetPort,
		}
	}
	return r
}

func (d *daemon) availableIntercepts(_ *supervisor.Process) *rpc.AvailableInterceptsResponse {
	r := &rpc.AvailableInterceptsResponse{}
	r.Error, r.Text = d.interceptStatus()
	if r.Error != rpc.InterceptError_InterceptOk {
		return r
	}
	is := d.trafficMgr.interceptables
	r.Intercepts = make([]*rpc.AvailableInterceptsResponse_ListEntry, len(is))
	for idx, deployment := range is {
		fields := strings.SplitN(deployment, "/", 2)
		var av *rpc.AvailableInterceptsResponse_ListEntry
		if len(fields) != 2 {
			av = &rpc.AvailableInterceptsResponse_ListEntry{
				Namespace:  d.cluster.namespace,
				Deployment: deployment,
			}
		} else {
			av = &rpc.AvailableInterceptsResponse_ListEntry{
				Namespace:  fields[0],
				Deployment: fields[1],
			}
		}
		r.Intercepts[idx] = av
	}
	return r
}

// addIntercept adds one intercept
func (d *daemon) addIntercept(p *supervisor.Process, ir *rpc.InterceptRequest) *rpc.InterceptResponse {
	r := &rpc.InterceptResponse{}
	if ir.Preview && d.trafficMgr.previewHost == "" {
		r.Error = rpc.InterceptError_NoPreviewHost
		return r
	}

	r.Error, r.Text = d.interceptStatus()
	if r.Error != rpc.InterceptError_InterceptOk {
		return r
	}

	for _, ic := range d.intercepts {
		if ic.ii.Name == ir.Name {
			r.Error = rpc.InterceptError_AlreadyExists
			r.Text = ir.Name
			return r
		}
	}

	// Do we already have a namespace?
	if ir.Namespace == "" {
		// Nope. See if we have an interceptable that matches the name.

		matches := make([][]string, 0)
		for _, deployment := range d.trafficMgr.interceptables {
			fields := strings.SplitN(deployment, "/", 2)
			if len(fields) != 2 {
				fields = []string{d.cluster.namespace, fields[0]}
			}

			if ir.Deployment == fields[1] {
				matches = append(matches, fields)
			}
		}

		switch len(matches) {
		case 0:
			r.Error = rpc.InterceptError_NoAcceptableDeployment
			r.Text = ir.Deployment
			return r

		case 1:
			// Good to go.
			ir.Namespace = matches[0][0]

		default:
			txt, _ := json.Marshal(matches)
			r.Error = rpc.InterceptError_AmbiguousMatch
			r.Text = string(txt)
			return r
		}
	}

	ii := &InterceptInfo{ir}
	ic, err := MakeIntercept(p, d.trafficMgr, d.cluster, ii)
	if err != nil {
		r.Error = rpc.InterceptError_FailedToEstablish
		r.Text = err.Error()
		return r
	}

	if d.trafficMgr.previewHost != "" {
		r.PreviewURL = ii.PreviewURL(d.trafficMgr.previewHost)
	}

	d.intercepts = append(d.intercepts, ic)

	// return OK status and the chosen namespace
	r.Text = ir.Namespace
	return r
}

// removeIntercept removes one intercept by name
func (d *daemon) removeIntercept(_ *supervisor.Process, name string) *rpc.InterceptResponse {
	r := &rpc.InterceptResponse{}
	for idx, cept := range d.intercepts {
		if cept.ii.Name == name {
			d.intercepts = append(d.intercepts[:idx], d.intercepts[idx+1:]...)
			if err := cept.Close(); err != nil {
				r.Error = rpc.InterceptError_FailedToRemove
				r.Text = err.Error()
			}
			return r
		}
	}
	r.Error, r.Text = d.interceptStatus()
	if r.Error == rpc.InterceptError_InterceptOk {
		r.Error = rpc.InterceptError_NotFound
		r.Text = name
	}
	return r
}

// ClearIntercepts removes all intercepts
func (d *daemon) ClearIntercepts(p *supervisor.Process) error {
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
		p.Logf("%v: Deleting mapping in namespace %v", cept.ii.Name, cept.ii.Namespace)
		del := cept.cluster.GetKubectlCmd(p, "delete", "-n", cept.ii.Namespace, "mapping", fmt.Sprintf("%s-mapping", cept.ii.Name))
		err = del.Run()
		p.Logf("%v: Deleted mapping in namespace %v", cept.ii.Name, cept.ii.Namespace)
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
	AmbassadorID  []string          `json:"ambassador_id"`
	Prefix        string            `json:"prefix"`
	Rewrite       string            `json:"rewrite"`
	Service       string            `json:"service"`
	RegexHeaders  map[string]string `json:"regex_headers"`
	GRPC          bool              `json:"grpc"`
	TimeoutMs     int               `json:"timeout_ms"`
	IdleTimeoutMs int               `json:"idle_timeout_ms"`
}

type interceptMapping struct {
	APIVersion string          `json:"apiVersion"`
	Kind       string          `json:"kind"`
	Metadata   mappingMetadata `json:"metadata"`
	Spec       mappingSpec     `json:"spec"`
}

// MakeIntercept acquires an intercept and returns a Resource handle
// for it
func MakeIntercept(p *supervisor.Process, tm *TrafficManager, cluster *KCluster, ii *InterceptInfo) (*Intercept, error) {
	port, err := ii.Acquire(p, tm)
	if err != nil {
		return nil, err
	}

	cept := &Intercept{ii: ii, tm: tm, cluster: cluster, port: port}
	cept.mappingExists = false
	cept.doCheck = cept.check
	cept.doQuit = cept.quit
	cept.setup(p.Supervisor(), ii.Name)

	p.Logf("%s: Intercepting via port %v, grpc %v, using namespace %v", ii.Name, port, ii.GRPC, ii.Namespace)

	mapping := interceptMapping{
		APIVersion: "getambassador.io/v2",
		Kind:       "Mapping",
		Metadata: mappingMetadata{
			Name:      fmt.Sprintf("%s-mapping", ii.Name),
			Namespace: ii.Namespace,
		},
		Spec: mappingSpec{
			AmbassadorID:  []string{fmt.Sprintf("intercept-%s", ii.Deployment)},
			Prefix:        ii.Prefix,
			Rewrite:       ii.Prefix,
			Service:       fmt.Sprintf("telepresence-proxy.%s:%d", tm.namespace, port),
			RegexHeaders:  ii.Patterns,
			GRPC:          ii.GRPC, // Set the grpc flag on the Intercept mapping
			TimeoutMs:     60000,   // Making sure we don't have shorter timeouts on intercepts than the original Mapping
			IdleTimeoutMs: 60000,
		},
	}

	manifest, err := json.MarshalIndent(&mapping, "", "  ")
	if err != nil {
		_ = cept.Close()
		return nil, errors.Wrap(err, "Intercept: mapping could not be constructed")
	}

	apply := cluster.GetKubectlCmdNoNamespace(p, "apply", "-f", "-")
	apply.Stdin = strings.NewReader(string(manifest))
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

	if err := cept.removeMapping(p); err != nil {
		p.Logf("cept.Quit failed to remove %v: %+v", cept.ii.Name, err)
	} else {
		p.Logf("cept.Quit removed %v", cept.ii.Name)
	}

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
