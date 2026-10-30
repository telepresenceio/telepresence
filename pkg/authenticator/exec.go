package authenticator

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

func ResolveExecConfig(ctx context.Context, execConfig *clientcmdapi.ExecConfig) ([]byte, error) {
	var buf bytes.Buffer

	cmd := proc.CommandContext(ctx, execConfig.Command, execConfig.Args...)
	cmd.Stdout = &buf
	cmd.Stderr = dos.Stderr(ctx)
	em := dos.FromEnvPairs(dos.Environ(ctx))
	for _, ev := range execConfig.Env {
		em[ev.Name] = ev.Value
	}
	if execConfig.APIVersion != "" {
		info := struct {
			APIVersion string `json:"apiVersion"`
			Kind       string `json:"kind"`
			Spec       struct {
				Interactive bool `json:"interactive"`
			} `json:"spec"`
		}{APIVersion: execConfig.APIVersion, Kind: "ExecCredential"}
		data, err := json.Marshal(info)
		if err != nil {
			return nil, fmt.Errorf("failed to encode exec credential input: %w", err)
		}
		em["KUBERNETES_EXEC_INFO"] = string(data)
	}
	cmd.Env = em.Environ()

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to run host command: %w", err)
	}

	return buf.Bytes(), nil
}
