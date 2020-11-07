package connector

import (
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
	Name       string
	Namespace  string
	Deployment string
	Patterns   map[string]string
}

/*

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
			Service:       fmt.Sprintf("traffic-manager:%d", port),
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
		"-p", strconv.Itoa(int(tm.sshPort)),
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

*/
