package cmd

import (
	"github.com/spf13/cobra"
	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

type listContextsCommand struct {
	rq *daemon.CobraRequest
}

func listContexts() *cobra.Command {
	lcc := &listContextsCommand{}

	cmd := &cobra.Command{
		Use:   "list-contexts",
		Args:  cobra.NoArgs,
		Short: "Show all contexts",
		RunE:  lcc.run,
	}
	lcc.rq = daemon.InitRequest(cmd)
	return cmd
}

type kubeCtx struct {
	*api.Context `yaml:",inline"`
	Current      bool `json:"current,omitempty"`
}

func (lcc *listContextsCommand) run(cmd *cobra.Command, _ []string) error {
	config, err := lcc.rq.GetConfig(cmd)
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	cm := make(map[string]kubeCtx, len(config.Contexts))
	for n, c := range config.Contexts {
		cm[n] = kubeCtx{Context: c, Current: n == config.CurrentContext}
	}

	if output.WantsFormatted(cmd) {
		output.Object(ctx, cm, false)
	} else {
		for n, c := range cm {
			pfx := '-'
			if c.Current {
				pfx = '*'
			}
			ioutil.Printf(output.Out(ctx), "%c name: %s\n  default namespace: %s\n", pfx, n, c.Namespace)
		}
	}
	return nil
}
