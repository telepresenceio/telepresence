package integration_test

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/telepresenceio/dlib/v2/dlog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

type proxyViaSuite struct {
	itest.Suite
	itest.TrafficManager
}

func (s *proxyViaSuite) SuiteName() string {
	return "ProxyVia"
}

func init() {
	itest.AddTrafficManagerSuite("", func(h itest.TrafficManager) itest.TestingSuite {
		return &proxyViaSuite{Suite: itest.Suite{Harness: h}, TrafficManager: h}
	})
}

const (
	domain   = "mydomain.local"
	alias    = "echo-home"
	fqnAlias = alias + "." + domain
)

func (s *proxyViaSuite) SetupSuite() {
	if !s.ClientIsVersion(">2.24.x") {
		s.T().Skip("ProxyVia have some quirks in versions <2.25.0")
	}
	s.Suite.SetupSuite()
	tpl := struct {
		AliasIP string
		Aliases []string
	}{
		AliasIP: "127.0.0.1",
		Aliases: []string{alias, fqnAlias},
	}
	if s.IsIPv6() {
		tpl.AliasIP = "::1"
	}
	s.ApplyTemplate(s.Context(), filepath.Join("testdata", "k8s", "echo-w-hostalias.goyaml"), &tpl)
	s.NoError(s.RolloutStatusWait(s.Context(), "deploy/echo"))
}

func (s *proxyViaSuite) TearDownSuite() {
	s.DeleteSvcAndWorkload(s.Context(), "deploy", "echo")
}

func (s *proxyViaSuite) Test_ProxyViaLoopBack() {
	ctx := s.Context()
	if s.IsIPv6() {
		ctx = itest.WithConfig(ctx, func(config client.Config) {
			config.Routing().VirtualSubnet = netip.MustParsePrefix("abac:0de0::/64")
		})
	}

	s.TelepresenceHelmInstallOK(ctx, true, "--set", "client.dns.includeSuffixes={mydomain.local}")
	defer s.RollbackTM(ctx)

	if s.IsIPv6() {
		s.TelepresenceConnect(ctx, "--proxy-via", "::1/128=echo")
	} else {
		s.TelepresenceConnect(ctx, "--proxy-via", "127.0.0.1/32=echo")
	}
	defer itest.TelepresenceQuitOk(ctx)
	s.CapturePodLogs(ctx, "echo", "traffic-agent", s.AppNamespace())

	virtualSubnet := client.GetConfig(ctx).Routing().VirtualSubnet

	tests := []struct {
		name           string
		hostName       string
		expectedOutput *regexp.Regexp
	}{
		{
			"single-label",
			alias,
			regexp.MustCompile("Host: " + alias + ":8080"),
		},
		{
			"fully-qualified",
			fqnAlias,
			regexp.MustCompile("Host: " + fqnAlias + ":8080"),
		},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			rq := s.Require()
			var vip netip.Addr
			rq.Eventually(func() bool {
				// hostname will resolve to 127.0.0.1 remotely and then be translated into a virtual IP
				ips, err := net.LookupIP(tt.hostName)
				if err != nil {
					dlog.Error(ctx, err)
					return false
				}
				if len(ips) != 1 {
					dlog.Error(ctx, "LookupIP did not return one IP")
					return false
				}
				var ok bool
				vip, ok = netip.AddrFromSlice(ips[0])
				return ok
			}, 30*time.Second, 2*time.Second)
			dlog.Infof(ctx, "%s uses IP %s", tt.hostName, vip)
			rq.Truef(virtualSubnet.Contains(vip), "virtualIPSubnet %s does not contain %s", virtualSubnet, vip)

			rq.Eventually(func() bool {
				out, err := itest.Output(ctx, "curl", "--silent", "--max-time", "2", net.JoinHostPort(tt.hostName, "8080"))
				dlog.Info(ctx, out)
				return err == nil && tt.expectedOutput.MatchString(out)
			}, 10*time.Second, 2*time.Second)
		})
	}
}

func (s *proxyViaSuite) Test_ProxyViaEverything() {
	ctx := s.Context()
	s.TelepresenceConnect(ctx)
	st := itest.TelepresenceStatusOk(ctx)
	itest.TelepresenceDisconnectOk(ctx)
	sns := st.RootDaemon.Subnets
	if len(sns) == 0 {
		s.T().Skip("Test cannot run unless client maps at least one subnet")
	}

	if s.IsIPv6() {
		ctx = itest.WithConfig(ctx, func(config client.Config) {
			config.Routing().VirtualSubnet = netip.MustParsePrefix("abac:0de0::/64")
		})
	}

	args := make([]string, 0, len(sns)*2)
	for _, sn := range sns {
		args = append(args, "--proxy-via", sn.String()+"=echo")
	}
	s.TelepresenceConnect(ctx, args...)
	st = itest.TelepresenceStatusOk(ctx)
	defer itest.TelepresenceDisconnectOk(ctx)
	rq := s.Require()
	rq.NotNil(st.RootDaemon)
	rq.Len(st.RootDaemon.Subnets, 1) // Virtual subnet
	rq.Eventually(func() bool {
		out, err := itest.Output(ctx, "curl", "--silent", "--max-time", "2", "echo")
		dlog.Infof(ctx, "Output from echo service %s", out)
		return err == nil
	}, 10*time.Second, 2*time.Second)
}

func (s *proxyViaSuite) Test_ProxyViaAll() {
	ctx := s.Context()
	rq := s.Require()
	if s.IsIPv6() {
		ctx = itest.WithConfig(ctx, func(config client.Config) {
			config.Routing().VirtualSubnet = netip.MustParsePrefix("abac:0de0::/64")
		})
	}

	s.TelepresenceConnect(ctx, "--proxy-via", "all=echo")
	st := itest.TelepresenceStatusOk(ctx)
	defer itest.TelepresenceDisconnectOk(ctx)
	rq.NotNil(st.RootDaemon)
	rq.Len(st.RootDaemon.Subnets, 1) // Virtual subnet
	rq.Eventually(func() bool {
		out, err := itest.Output(ctx, "curl", "--silent", "--max-time", "2", "echo")
		dlog.Infof(ctx, "Output from echo service %s", out)
		return err == nil
	}, 30*time.Second, 2*time.Second)
}

func (s *proxyViaSuite) Test_ProxyViaAllAndMounts() {
	if s.IsCI() && runtime.GOOS == "darwin" {
		s.T().Skip("CI can't do user mounts on darwin")
	}
	ctx := s.Context()
	rq := s.Require()
	if s.IsIPv6() {
		ctx = itest.WithConfig(ctx, func(config client.Config) {
			config.Routing().VirtualSubnet = netip.MustParsePrefix("abac:0de0::/64")
		})
	}

	s.TelepresenceConnect(ctx, "--proxy-via", "all=echo")
	st := itest.TelepresenceStatusOk(ctx)
	defer itest.TelepresenceDisconnectOk(ctx)
	rq.NotNil(st.RootDaemon)
	rq.Len(st.RootDaemon.Subnets, 1)

	var mountPoint string
	if runtime.GOOS == "windows" {
		mountPoint = "T:"
	} else {
		var err error
		mountPoint, err = os.MkdirTemp("", "mount-") // Don't use the testing.Tempdir() because deletion is delayed.
		s.Require().NoError(err)
		defer func() {
			time.AfterFunc(time.Second, func() {
				_ = os.RemoveAll(mountPoint)
			})
		}()
	}

	itest.TelepresenceOk(ctx, "intercept", "--mount", mountPoint, "echo")

	// Verify that volume mount is present and functional
	time.Sleep(3 * time.Second) // avoid a stat just when the intercept became active as it sometimes causes a hang
	fst, err := os.Stat(filepath.Join(mountPoint, "var"))
	rq.NoError(err, "Stat on <mount point>/var failed")
	rq.True(fst.IsDir())
}

func (s *proxyViaSuite) Test_NeverProxySubnetIsOmitted() {
	ctx := s.Context()
	s.TelepresenceConnect(ctx)
	st := itest.TelepresenceStatusOk(ctx)
	itest.TelepresenceDisconnectOk(ctx)
	sns := st.RootDaemon.Subnets
	if len(sns) == 0 {
		s.T().Skip("Test cannot run unless client maps at least one subnet")
	}
	logFile := filepath.Join(filelocation.AppUserLogDir(s.Context()), "daemon.log")
	rootLog, err := os.Open(logFile)
	s.Require().NoError(err)
	defer rootLog.Close()

	for _, sn := range sns {
		s.Run("subnet "+sn.String(), func() {
			ctx := s.Context()
			rq := s.Require()

			fs, err := rootLog.Stat()
			rq.NoError(err)
			pos := fs.Size()

			s.TelepresenceConnect(ctx, "--never-proxy", sn.String())
			itest.TelepresenceDisconnectOk(ctx)
			_, err = rootLog.Seek(pos, io.SeekStart)
			rq.NoError(err)
			scn := bufio.NewScanner(rootLog)
			found := false

			msg := fmt.Sprintf("Dropping never-proxy %q", sn.String())
			for scn.Scan() {
				txt := scn.Text()
				if strings.Contains(txt, msg) {
					found = true
					break
				}
			}
			s.Truef(found, "Unable to find %q", msg)
		})
	}
	s.Run(fmt.Sprintf("subnets %s", sns), func() {
		ctx := s.Context()
		rq := s.Require()

		fs, err := rootLog.Stat()
		rq.NoError(err)
		pos := fs.Size()

		sb := strings.Builder{}
		for i, sn := range sns {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(sn.String())
		}
		s.TelepresenceConnect(ctx, "--never-proxy", sb.String())
		itest.TelepresenceDisconnectOk(ctx)
		for _, sn := range sns {
			_, err = rootLog.Seek(pos, io.SeekStart)
			rq.NoError(err)
			scn := bufio.NewScanner(rootLog)
			found := false

			msg := fmt.Sprintf("Dropping never-proxy %q", sn.String())
			for scn.Scan() {
				txt := scn.Text()
				if strings.Contains(txt, msg) {
					found = true
					break
				}
			}
			s.Truef(found, "Unable to find %q", msg)
		}
	})
}
