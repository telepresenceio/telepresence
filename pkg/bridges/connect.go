package bridges

import (
	"strings"

	"github.com/datawire/ambassador/pkg/k8s"
	"github.com/datawire/ambassador/pkg/supervisor"
)

// worker names
const (
	BridgeWorker         = "BRG"
	K8sBridgeWorker      = "K8S"
	K8sPortForwardWorker = "KPF"
	K8sSSHWorker         = "SSH"
	K8sApplyWorker       = "KAP"
	DkrBridgeWorker      = "DKR"
)

const podManifest = `
---
apiVersion: v1
kind: Pod
metadata:
  name: teleproxy
  labels:
    name: teleproxy
spec:
  hostname: traffic-proxy
  containers:
  - name: proxy
    image: docker.io/datawire/telepresence-k8s:0.75
    ports:
    - protocol: TCP
      containerPort: 8022
`

// config holds the configuration for a Teleproxy
type config struct {
	kubeConfig string
	context    string
	namespace  string
	workers    []*supervisor.Worker
}

func (t *config) Restart() {
	for _, w := range t.workers {
		w.Shutdown()
	}
	for _, w := range t.workers {
		w.Wait()
	}
	for _, w := range t.workers {
		w.Restart()
	}
}

type Service interface {
	// Check that the service is in good health
	Check(p *supervisor.Process) bool

	// Start the bridges service
	Start(p *supervisor.Process) error

	// Restart all workers
	Restart()
}

func NewService(kubeConfig, context, namespace string) Service {
	return &config{
		kubeConfig: kubeConfig,
		context:    context,
		namespace:  namespace,
	}
}

func (t *config) addWorker(p *supervisor.Process, worker *supervisor.Worker) {
	p.Supervisor().Supervise(worker)
	t.workers = append(t.workers, worker)
}

func (t *config) connect(p *supervisor.Process) {
	t.addWorker(p, &supervisor.Worker{
		Name: K8sApplyWorker,
		Work: func(p *supervisor.Process) (err error) {
			kubeInfo := k8s.NewKubeInfo(t.kubeConfig, t.context, t.namespace)
			// setup remote teleproxy pod
			args, err := kubeInfo.GetKubectlArray("apply", "-f", "-")
			if err != nil {
				return err
			}
			apply := p.Command("kubectl", args...)
			apply.Stdin = strings.NewReader(podManifest)
			err = apply.Start()
			if err != nil {
				return
			}
			err = p.DoClean(apply.Wait, apply.Process.Kill)
			if err != nil {
				return
			}
			p.Ready()
			// we need to stay alive so that our dependencies can start
			<-p.Shutdown()
			return
		},
	})

	t.addWorker(p, &supervisor.Worker{
		Name:     K8sPortForwardWorker,
		Requires: []string{K8sApplyWorker},
		Retry:    true,
		Work: func(p *supervisor.Process) (err error) {
			kubeInfo := k8s.NewKubeInfo(t.kubeConfig, t.context, t.namespace)
			args, err := kubeInfo.GetKubectlArray("port-forward", "pod/teleproxy", "8022")
			if err != nil {
				return err
			}
			pf := p.Command("kubectl", args...)
			err = pf.Start()
			if err != nil {
				return
			}
			p.Ready()
			err = p.DoClean(func() error {
				err := pf.Wait()
				if err != nil {
					args, err := kubeInfo.GetKubectlArray("get", "pod/teleproxy")
					if err != nil {
						return err
					}
					inspect := p.Command("kubectl", args...)
					_ = inspect.Run() // Discard error as this is just for logging
				}
				return err
			}, func() error {
				return pf.Process.Kill()
			})
			return
		},
	})

	t.addWorker(p, &supervisor.Worker{
		Name:     K8sSSHWorker,
		Requires: []string{K8sPortForwardWorker},
		Retry:    true,
		Work: func(p *supervisor.Process) (err error) {
			// XXX: probably need some kind of keepalive check for ssh, first
			// curl after wakeup seems to trigger detection of death
			ssh := p.Command("ssh", "-D", "localhost:1080", "-C", "-N", "-oConnectTimeout=5",
				"-oExitOnForwardFailure=yes", "-oStrictHostKeyChecking=no",
				"-oUserKnownHostsFile=/dev/null", "telepresence@localhost", "-p", "8022")
			err = ssh.Start()
			if err != nil {
				return
			}
			p.Ready()
			return p.DoClean(ssh.Wait, ssh.Process.Kill)
		},
	})
}
