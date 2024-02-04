package integration_test

import (
	"encoding/json"

	core "k8s.io/api/core/v1"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

func (s *connectedSuite) Test_PrefixInterpolated() {
	ctx := s.Context()
	svc := "echo-interpolate"
	rq := s.Require()
	s.ApplyApp(ctx, svc, "deploy/"+svc)
	defer func() {
		s.DeleteSvcAndWorkload(ctx, "deploy", svc)
		s.NoError(s.Kubectl(ctx, "delete", "configmap", "interpolate-config"))
	}()

	itest.TelepresenceOk(ctx, "intercept", "--mount", "false", svc)
	defer itest.TelepresenceOk(ctx, "leave", svc)
	out, err := s.KubectlOut(ctx, "get", "pod", "-o", "json", "-l", "app="+svc)
	rq.NoError(err)

	var pods core.PodList
	err = json.Unmarshal([]byte(out), &pods)
	rq.NoError(err)
	var ag *core.Container
outer:
	for _, pod := range pods.Items {
		cns := pod.Spec.Containers
		for ci := range cns {
			cn := &cns[ci]
			if cn.Name == "traffic-agent" {
				ag = cn
				break outer
			}
		}
	}
	rq.NotNil(ag)
	for _, vm := range ag.VolumeMounts {
		if vm.Name == "my-volume" {
			rq.Equal("$(_TEL_APP_A_SOME_NAME)_$(_TEL_APP_A_OTHER_NAME)", vm.SubPathExpr)
			break
		}
	}
}
