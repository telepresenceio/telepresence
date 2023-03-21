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

	for i := range execConfig.Env {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", execConfig.Env[i].Name, execConfig.Env[i].Value))
	}

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to run host command: %w", err)
	}

	return buf.Bytes(), nil
}
