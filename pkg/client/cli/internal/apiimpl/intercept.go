package apiimpl

import (
	"strings"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/api"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/intercept"
)

func toInterceptCmd(rq *api.InterceptRequest, ih api.InterceptHandler) *intercept.Command {
	cmd := &intercept.Command{
		Name:           rq.Name,
		AgentName:      rq.WorkloadName,
		Port:           rq.Port,
		ServiceName:    rq.ServiceName,
		LocalMountPort: rq.LocalMountPort,
		Replace:        rq.Replace,
		EnvFile:        rq.EnvFile,
		EnvJSON:        rq.EnvJSON,
		ToPod:          toStrings(rq.ToPod),
		Mechanism:      "tcp",
	}
	if cmd.Name == "" {
		cmd.Name = cmd.AgentName
	}

	if rq.Address.IsValid() {
		cmd.Address = rq.Address.String()
	} else {
		cmd.Address = "127.0.0.1"
	}
	switch ih := ih.(type) {
	case nil:
	case api.CmdHandler:
		cmd.Cmdline = ih.Cmdline
		cmd.Mount, cmd.MountSet = toCmdMount(ih.MountPoint)
	case api.DockerRunInterceptHandler:
		cmd.DockerRun = true
		cmd.Cmdline = appendOptions(ih.Options, cmd.Cmdline)
		cmd.Cmdline = append(cmd.Cmdline, ih.Image)
		cmd.Cmdline = append(cmd.Cmdline, ih.Arguments...)
		cmd.Mount = "true"
	case api.DockerBuildInterceptHandler:
		if ih.Debug {
			cmd.DockerDebug = ih.Context
		} else {
			cmd.DockerBuild = ih.Context
		}
		cmd.DockerBuildOptions = ih.BuildOptions
		cmd.Cmdline = appendOptions(ih.Options, cmd.Cmdline)
		cmd.Cmdline = append(cmd.Cmdline, "IMAGE")
		cmd.Cmdline = append(cmd.Cmdline, ih.Arguments...)
		cmd.Mount = "true"
	}
	return cmd
}

func toCmdMount(mountPoint string) (string, bool) {
	switch mountPoint {
	case "", "false":
		return "false", true
	case "true":
		return "true", false
	default:
		return mountPoint, true
	}
}

func appendOptions(opts []string, flags []string) []string {
	for _, opt := range opts {
		if n := strings.IndexByte(opt, '='); n > 0 {
			flags = append(flags, "--"+opt[:n], opt[n+1:])
		} else {
			flags = append(flags, "--"+opt)
		}
	}
	return flags
}
