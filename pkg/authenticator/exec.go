package authenticator

import (
	"bytes"
	"context"
	"fmt"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

type execCredentialBinary struct{}

func (e execCredentialBinary) Resolve(
	ctx context.Context,
	execConfig *clientcmdapi.ExecConfig,
) ([]byte, error) {
	var buf bytes.Buffer

	cmd := proc.CommandContext(ctx, execConfig.Command, execConfig.Args...)
	cmd.Stdout = &buf
	cmd.Stderr = dos.Stderr(ctx)
	cmd.DisableLogging = true
	cmd.Env = dos.Environ(ctx)
	if len(execConfig.Env) > 0 {
		em := dos.FromEnvPairs(cmd.Env)
		for _, ev := range execConfig.Env {
			em[ev.Name] = ev.Value
		}
		cmd.Env = em.Environ()
	}

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to run host command: %w", err)
	}

	return buf.Bytes(), nil
}
