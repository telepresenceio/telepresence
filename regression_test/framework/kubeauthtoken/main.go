// Command kubeauthtoken is a kubeconfig `exec` credential plugin used by the
// connect area's KubeAuth suite. It prints the bearer token held in a file as
// an ExecCredential whose expirationTimestamp lies a given number of seconds
// after the moment the plugin runs, so every run yields a fresh expiry.
//
// Usage: kubeauthtoken <token file> <seconds until expiry>
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

func main() {
	if err := run(os.Args, os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func run(args []string, out *os.File) error {
	if len(args) != 3 {
		return fmt.Errorf("usage: %s <token file> <seconds until expiry>", args[0])
	}
	token, err := os.ReadFile(args[1])
	if err != nil {
		return err
	}
	secs, err := strconv.Atoi(args[2])
	if err != nil {
		return fmt.Errorf("invalid expiry %q: %w", args[2], err)
	}
	return json.NewEncoder(out).Encode(map[string]any{
		"apiVersion": "client.authentication.k8s.io/v1beta1",
		"kind":       "ExecCredential",
		"status": map[string]string{
			"token":               strings.TrimSpace(string(token)),
			"expirationTimestamp": time.Now().Add(time.Duration(secs) * time.Second).UTC().Format(time.RFC3339),
		},
	})
}
