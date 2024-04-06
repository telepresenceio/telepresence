package apiimpl

import (
	"context"
	"fmt"

	"github.com/distribution/reference"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	daemon2 "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/api"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/intercept"
)

type connection struct {
	context.Context
}

func (c connection) Namespace() string {
	return daemon.GetSession(c).Info.Namespace
}

func (c connection) AgentImage() (reference.Reference, error) {
	fqn, err := daemon.GetSession(c).AgentImageFQN(c, &emptypb.Empty{})
	if err != nil {
		return nil, err
	}
	return reference.Parse(fqn.FQN)
}

func (c connection) Close() error {
	return daemon.GetUserClient(c).Conn.Close()
}

func (c connection) Disconnect() error {
	_, err := daemon.GetSession(c).Disconnect(c, &emptypb.Empty{})
	return err
}

func (c connection) Info() *connector.ConnectInfo {
	return daemon.GetSession(c).Info
}

func (c connection) DaemonInfo() (*daemon.Info, error) {
	return daemon.LoadInfo(c, daemon.GetSession(c).DaemonID.InfoFileName())
}

func (c connection) List(namespace string) ([]*connector.WorkloadInfo, error) {
	if namespace == "" {
		namespace = c.Info().Namespace
	}
	wis, err := daemon.GetSession(c).List(c, &connector.ListRequest{
		Namespace: namespace,
		Filter:    connector.ListRequest_EVERYTHING,
	})
	if err != nil {
		return nil, err
	}
	return wis.Workloads, nil
}

func (c connection) StartIntercept(rq api.InterceptRequest, mountPoint string) (*intercept.Info, error) {
	ic := toInterceptCmd(&rq, nil)
	ic.Mount, ic.MountSet = toCmdMount(mountPoint)
	return intercept.NewState(ic).Run(c)
}

func (c connection) RunIntercept(rq api.InterceptRequest, handler api.InterceptHandler) (*intercept.Info, error) {
	ic := toInterceptCmd(&rq, handler)
	return intercept.NewState(ic).Run(c)
}

func (c connection) EndIntercept(name string) error {
	s := daemon.GetSession(c)
	_, err := s.RemoveIntercept(c, &manager.RemoveInterceptRequest2{
		Session: s.Info.SessionInfo,
		Name:    name,
	})
	return err
}

func toDaemonSnw(sns []api.SubnetViaWorkload) []*daemon2.SubnetViaWorkload {
	if len(sns) == 0 {
		return nil
	}
	dsn := make([]*daemon2.SubnetViaWorkload, len(sns))
	for i, sn := range sns {
		dsn[i] = &daemon2.SubnetViaWorkload{
			Subnet:   sn.Subnet,
			Workload: sn.Workload,
		}
	}
	return dsn
}

func toDaemonRequest(cr *api.ConnectRequest) *daemon.Request {
	return &daemon.Request{
		ConnectRequest: connector.ConnectRequest{
			KubeFlags:               cr.KubeFlags,
			KubeconfigData:          cr.KubeConfigData,
			Name:                    cr.Name,
			MappedNamespaces:        cr.MappedNamespaces,
			ManagerNamespace:        cr.ManagerNamespace,
			AlsoProxy:               toStrings(cr.AlsoProxy),
			NeverProxy:              toStrings(cr.NeverProxy),
			AllowConflictingSubnets: toStrings(cr.AllowConflictingSubnets),
			SubnetViaWorkloads:      toDaemonSnw(cr.SubnetViaWorkloads),
		},
		Docker:                  cr.Docker,
		ExposedPorts:            cr.ExposedPorts,
		Hostname:                cr.Hostname,
		UserDaemonProfilingPort: cr.UserDaemonProfilingPort,
		RootDaemonProfilingPort: cr.RootDaemonProfilingPort,
	}
}

func toStrings[T fmt.Stringer](stringers []T) (ss []string) {
	if ln := len(stringers); ln > 0 {
		ss = make([]string, ln)
		for i, s := range stringers {
			ss[i] = s.String()
		}
	}
	return
}
