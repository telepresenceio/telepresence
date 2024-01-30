package integration_test

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

func (s *notConnectedSuite) Test_ConnectWithKubeconfigExec() {
	ctx := s.Context()
	rq := s.Require()
	kc := itest.KubeConfig(ctx)
	cfg, err := clientcmd.LoadFromFile(kc)
	rq.NoError(err)

	// Create an additional context that has a user with an exec extension. The extension calls the k8screds program
	// to retrieve the credentials for the original context.
	rq.NotEmpty(cfg.CurrentContext)
	cc := cfg.Contexts[cfg.CurrentContext]
	rq.NotNil(cc)
	rq.NotEmpty(cc.AuthInfo)
	ai := cfg.AuthInfos[cc.AuthInfo]
	rq.NotNil(ai)
	if ai.Exec != nil {
		s.T().Skipf("this test requires a kubecontext that doesn't have an exec extension")
	}

	// Ensure that the k8screds program is built and ready.
	binDir := s.T().TempDir()
	k8sCredsBinary := filepath.Join(binDir, "k8screds")
	if runtime.GOOS == "windows" {
		k8sCredsBinary += ".exe"
	}
	rq.NoError(itest.Run(ctx, "go", "build", "-o", k8sCredsBinary, filepath.Join("testdata", "k8screds", "main.go")))

	// Create a new AuthInfo.
	extAuthInfo := cc.AuthInfo + "-exec"
	rq.Nil(cfg.AuthInfos[extAuthInfo])

	envMap := itest.EnvironMap(ctx)
	envVars := make([]api.ExecEnvVar, len(envMap))
	i := 0
	for k, v := range envMap {
		envVars[i].Name = k
		envVars[i].Value = v
		i++
	}

	cfg.AuthInfos[extAuthInfo] = &api.AuthInfo{
		Exec: &api.ExecConfig{
			Command:          k8sCredsBinary,
			Args:             []string{cfg.CurrentContext},
			Env:              envVars,
			APIVersion:       "client.authentication.k8s.io/v1beta1",
			InteractiveMode:  "Never",
			StdinUnavailable: true,
		},
	}

	extCc := cc.DeepCopy()
	extCc.AuthInfo = extAuthInfo

	// Create a new Context that uses the new AuthInfo. We use a nasty name here to ensure
	// that it is correctly converted to a usable name.
	extContext := "abc:def/xyz$32-#1?efd"
	rq.Nil(cfg.Contexts[extContext])

	cfg.Contexts[extContext] = extCc
	cfg.CurrentContext = extContext

	connectWithExec := func(connectFromUserDaemon, useDocker bool) {
		if useDocker && s.IsCI() {
			if !(runtime.GOOS == "linux" && runtime.GOARCH == "amd64") {
				s.T().Skip("CI can't run linux docker containers inside non-linux runners")
			}
		}

		// Retrieve the current size of the connector.lgo so that we can scan the messages that appear after connect
		ctx := s.Context()
		rq := s.Require()
		logSize := int64(0)
		logName := "connector.log"
		if useDocker {
			// Authenticator runs as a separate process on the host
			logName = "kubeauth.log"
		}
		logFQName := filepath.Join(filelocation.AppUserLogDir(ctx), logName)
		st, err := os.Stat(logFQName)
		if err == nil {
			logSize = st.Size()
		} else if !errors.Is(err, os.ErrNotExist) {
			rq.FailNow(err.Error())
		}

		ctx = itest.WithKubeConfig(ctx, cfg)
		if connectFromUserDaemon {
			ctx = itest.WithConfig(ctx, func(conf client.Config) {
				conf.Cluster().ConnectFromRootDaemon = false
			})
		}

		var args []string
		if useDocker {
			args = []string{"--docker"}
		}
		s.TelepresenceConnect(ctx, args...)
		defer itest.TelepresenceQuitOk(ctx)

		// Scan the log from its previous end. It should now contain a message indicating that the gRPC service that
		// it contains have served an exec request from a modified kubeconfig requesting credentials from extContext.
		logF, err := os.Open(logFQName)
		rq.NoError(err)
		defer logF.Close()
		if logSize > 0 {
			_, err = logF.Seek(logSize, 0)
			rq.NoError(err)
		}
		scn := bufio.NewScanner(logF)
		found := false
		for !found && scn.Scan() {
			found = strings.Contains(scn.Text(), "GetContextExecCredentials("+extContext+")")
		}
		if connectFromUserDaemon {
			rq.Falsef(found, "did not expect a GetContextExecCredentials in the %s", logName)
		} else {
			rq.Truef(found, "unable to find expected GetContextExecCredentials in the %s", logName)
		}

		modifiedKubeConfig := filepath.Join(filelocation.AppUserCacheDir(ctx), "kube", ioutil.SafeName(extContext))
		modCfg, err := clientcmd.LoadFromFile(modifiedKubeConfig)
		if connectFromUserDaemon {
			rq.ErrorIsf(err, os.ErrNotExist, "did not expect to find modified kubeconfig %s", modifiedKubeConfig)
		} else {
			rq.NoError(err)
			defer func() {
				_ = os.Remove(modifiedKubeConfig)
			}()
			rq.Equal(modCfg.CurrentContext, extContext)
		}
	}
	s.Run("root-daemon", func() { connectWithExec(false, false) })
	s.Run("user-daemon", func() { connectWithExec(true, false) })
	s.Run("containerized-daemon", func() { connectWithExec(false, true) })
}
