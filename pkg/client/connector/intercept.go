package connector

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/datawire/ambassador/pkg/supervisor"
	"github.com/pkg/errors"

	"github.com/datawire/telepresence2/pkg/client"
	rpc "github.com/datawire/telepresence2/pkg/rpc/connector"
)

func (s *service) interceptStatus() (rpc.InterceptError, string) {
	ie := rpc.InterceptError_UNSPECIFIED
	msg := ""
	switch {
	case s.cluster == nil:
		ie = rpc.InterceptError_NO_CONNECTION
	case s.trafficMgr == nil:
		ie = rpc.InterceptError_NO_TRAFFIC_MANAGER
	case !s.trafficMgr.IsOkay():
		if s.trafficMgr.apiErr != nil {
			ie = rpc.InterceptError_TRAFFIC_MANAGER_ERROR
			msg = s.trafficMgr.apiErr.Error()
		} else {
			ie = rpc.InterceptError_TRAFFIC_MANAGER_CONNECTING
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

// previewURL returns the Service Preview URL for this intercept if it is
// configured appropriately, or the empty string otherwise.
func (ii *InterceptInfo) previewURL(hostname string) (url string) {
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

// acquire an intercept from the traffic manager
func (ii *InterceptInfo) acquire(_ *supervisor.Process, tm *trafficManager) (int, error) {
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

// retain the given intercept. This likely needs to be called every
// five seconds or so.
func (ii *InterceptInfo) retain(_ *supervisor.Process, tm *trafficManager, port int) error {
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

// release the given intercept.
func (ii *InterceptInfo) release(_ *supervisor.Process, tm *trafficManager, port int) error {
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
func (s *service) listIntercepts(_ *supervisor.Process) *rpc.InterceptList {
	r := &rpc.InterceptList{}
	r.Error, r.Text = s.interceptStatus()
	if r.Error != rpc.InterceptError_UNSPECIFIED {
		return r
	}
	r.Intercepts = make([]*rpc.InterceptList_ListEntry, len(s.intercepts))
	for idx, cept := range s.intercepts {
		ii := cept.ii
		r.Intercepts[idx] = &rpc.InterceptList_ListEntry{
			Name:       ii.Name,
			Namespace:  ii.Namespace,
			Deployment: ii.Deployment,
			PreviewUrl: ii.previewURL(s.trafficMgr.previewHost),
			Patterns:   ii.Patterns,
			TargetHost: ii.TargetHost,
			TargetPort: ii.TargetPort,
		}
	}
	return r
}

func (s *service) availableIntercepts(_ *supervisor.Process) *rpc.AvailableInterceptList {
	r := &rpc.AvailableInterceptList{}
	r.Error, r.Text = s.interceptStatus()
	if r.Error != rpc.InterceptError_UNSPECIFIED {
		return r
	}
	is := s.trafficMgr.interceptables
	r.Intercepts = make([]*rpc.AvailableInterceptList_ListEntry, len(is))
	for idx, deployment := range is {
		fields := strings.SplitN(deployment, "/", 2)
		var av *rpc.AvailableInterceptList_ListEntry
		if len(fields) != 2 {
			av = &rpc.AvailableInterceptList_ListEntry{
				Namespace:  s.cluster.namespace,
				Deployment: deployment,
			}
		} else {
			av = &rpc.AvailableInterceptList_ListEntry{
				Namespace:  fields[0],
				Deployment: fields[1],
			}
		}
		r.Intercepts[idx] = av
	}
	return r
}

// addIntercept adds one intercept
func (s *service) addIntercept(p *supervisor.Process, ir *rpc.InterceptRequest) *rpc.Intercept {
	r := &rpc.Intercept{}
	if ir.Preview && s.trafficMgr.previewHost == "" {
		r.Error = rpc.InterceptError_NO_PREVIEW_HOST
		return r
	}

	r.Error, r.Text = s.interceptStatus()
	if r.Error != rpc.InterceptError_UNSPECIFIED {
		return r
	}

	for _, ic := range s.intercepts {
		if ic.ii.Name == ir.Name {
			r.Error = rpc.InterceptError_ALREADY_EXISTS
			r.Text = ir.Name
			return r
		}
	}

	// Do we already have a namespace?
	if ir.Namespace == "" {
		// Nope. See if we have an interceptable that matches the name.

		matches := make([][]string, 0)
		for _, deployment := range s.trafficMgr.interceptables {
			fields := strings.SplitN(deployment, "/", 2)
			if len(fields) != 2 {
				fields = []string{s.cluster.namespace, fields[0]}
			}

			if ir.Deployment == fields[1] {
				matches = append(matches, fields)
			}
		}

		switch len(matches) {
		case 0:
			r.Error = rpc.InterceptError_NO_ACCEPTABLE_DEPLOYMENT
			r.Text = ir.Deployment
			return r

		case 1:
			// Good to go.
			ir.Namespace = matches[0][0]

		default:
			txt, _ := json.Marshal(matches)
			r.Error = rpc.InterceptError_AMBIGUOUS_MATCH
			r.Text = string(txt)
			return r
		}
	}

	ii := &InterceptInfo{ir}
	ic, err := makeIntercept(p, s.trafficMgr, s.cluster, ii)
	if err != nil {
		r.Error = rpc.InterceptError_FAILED_TO_ESTABLISH
		r.Text = err.Error()
		return r
	}

	if s.trafficMgr.previewHost != "" {
		r.PreviewUrl = ii.previewURL(s.trafficMgr.previewHost)
	}

	s.intercepts = append(s.intercepts, ic)

	// return OK status and the chosen namespace
	r.Text = ir.Namespace
	return r
}

// removeIntercept removes one intercept by name
func (s *service) removeIntercept(_ *supervisor.Process, name string) *rpc.Intercept {
	r := &rpc.Intercept{}
	for idx, cept := range s.intercepts {
		if cept.ii.Name == name {
			s.intercepts = append(s.intercepts[:idx], s.intercepts[idx+1:]...)
			if err := cept.Close(); err != nil {
				r.Error = rpc.InterceptError_FAILED_TO_REMOVE
				r.Text = err.Error()
			}
			return r
		}
	}
	r.Error, r.Text = s.interceptStatus()
	if r.Error == rpc.InterceptError_UNSPECIFIED {
		r.Error = rpc.InterceptError_NOT_FOUND
		r.Text = name
	}
	return r
}

// clearIntercepts removes all intercepts
func (s *service) clearIntercepts(p *supervisor.Process) {
	for _, cept := range s.intercepts {
		if err := cept.Close(); err != nil {
			p.Logf("Closing intercept %q: %v", cept.ii.Name, err)
		}
	}
	s.intercepts = s.intercepts[:0]
}

// intercept is a Resource handle that represents a live intercept
type intercept struct {
	ii            *InterceptInfo
	tm            *trafficManager
	cluster       *k8sCluster
	port          int
	crc           client.Resource
	mappingExists bool
	client.ResourceBase
}

// removeMapping drops an Intercept's mapping if needed (and possible).
func (cept *intercept) removeMapping(p *supervisor.Process) error {
	var err error
	err = nil

	if cept.mappingExists {
		p.Logf("%v: Deleting mapping in namespace %v", cept.ii.Name, cept.ii.Namespace)
		del := cept.cluster.getKubectlCmd(p, "delete", "-n", cept.ii.Namespace, "mapping", fmt.Sprintf("%s-mapping", cept.ii.Name))
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

// makeIntercept acquires an intercept and returns a Resource handle
// for it
func makeIntercept(p *supervisor.Process, tm *trafficManager, cluster *k8sCluster, ii *InterceptInfo) (*intercept, error) {
	port, err := ii.acquire(p, tm)
	if err != nil {
		return nil, err
	}

	cept := &intercept{ii: ii, tm: tm, cluster: cluster, port: port}
	cept.mappingExists = false
	cept.Setup(p.Supervisor(), ii.Name, cept.check, cept.quit)

	p.Logf("%s: Intercepting via port %v, grpc %v, using namespace %v", ii.Name, port, ii.Grpc, ii.Namespace)

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
			GRPC:          ii.Grpc, // Set the grpc flag on the intercept mapping
			TimeoutMs:     60000,   // Making sure we don't have shorter timeouts on intercepts than the original Mapping
			IdleTimeoutMs: 60000,
		},
	}

	manifest, err := json.MarshalIndent(&mapping, "", "  ")
	if err != nil {
		_ = cept.Close()
		return nil, errors.Wrap(err, "Intercept: mapping could not be constructed")
	}

	apply := cluster.getKubectlCmdNoNamespace(p, "apply", "-f", "-")
	apply.Stdin = strings.NewReader(string(manifest))
	err = apply.Run()

	if err != nil {
		p.Logf("%v: Intercept could not apply mapping: %v", ii.Name, err)
		_ = cept.Close()
		return nil, errors.Wrap(err, "Intercept: kubectl apply")
	}

	cept.mappingExists = true

	sshArgs := []string{
		"-C", "-N", "telepresence@localhost",
		"-oConnectTimeout=10", "-oExitOnForwardFailure=yes",
		"-oStrictHostKeyChecking=no", "-oUserKnownHostsFile=/dev/null",
		"-p", strconv.Itoa(tm.sshPort),
		"-R", fmt.Sprintf("%d:%s:%d", cept.port, ii.TargetHost, ii.TargetPort),
	}

	p.Logf("%s: starting SSH tunnel", ii.Name)

	ssh, err := client.CheckedRetryingCommand(p, ii.Name+"-ssh", "ssh", sshArgs, nil, 5*time.Second)
	if err != nil {
		_ = cept.Close()
		return nil, err
	}

	cept.crc = ssh

	return cept, nil
}

func (cept *intercept) check(p *supervisor.Process) error {
	return cept.ii.retain(p, cept.tm, cept.port)
}

func (cept *intercept) quit(p *supervisor.Process) error {
	cept.SetDone()

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

	if err := cept.ii.release(p, cept.tm, cept.port); err != nil {
		p.Log(err)
	}

	p.Logf("cept.Quit released %v", cept.ii.Name)

	return nil
}
