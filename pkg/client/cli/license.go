package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
)

func LicenseCommand() *cobra.Command {
	var flags struct {
		outputFile string
	}
	cmd := &cobra.Command{
		Use:  "license [flags] <license_id>",
		Args: cobra.ExactArgs(1),

		Short: "Get License from Ambassador Cloud",
		Long:  "Get License from Ambassador Cloud",
		RunE: func(cmd *cobra.Command, args []string) error {
			return getCloudLicense(cmd.Context(), cmd.OutOrStdout(),
				args[0], flags.outputFile)
		},
	}

	cmd.Flags().StringVarP(&flags.outputFile, "output-file", "o", "", "The file where you want the license secret to be output to")
	return cmd
}

// getCloudLicense communicates with system a, acquires the jwt formatted license
// given by the id, places it in a secret, and outputs it to stdout or writes it
// to a file given by the user
func getCloudLicense(ctx context.Context, stdout io.Writer, id, outputFile string) error {
	license, hostDomain, err := cliutil.GetCloudLicense(ctx, outputFile, id)
	if err != nil {
		return err
	}

	writer := stdout
	// If a user gives a file, we write to the file instead of stdout
	if outputFile != "" {
		f, err := os.Create(outputFile)
		if err != nil {
			return err
		}
		defer f.Close()
		fmt.Fprintf(stdout, "Writing secret to %v", outputFile)
		writer = f
	}
	if err := createSecretFromLicense(ctx, writer, license, hostDomain); err != nil {
		return err
	}
	return nil
}

// Creates the kubernetes secret that can be put in your cluster
// to access licensed features if the cluster is airgapped and
// writes it to the given writer
func createSecretFromLicense(ctx context.Context, writer io.Writer, license, hostDomain string) error {
	secret := &kates.Secret{
		TypeMeta: kates.TypeMeta{
			Kind:       "Secret",
			APIVersion: "v1",
		},
		ObjectMeta: kates.ObjectMeta{
			Namespace: "ambassador",
			Name:      "systema-license",
		},
		Data: map[string][]byte{
			"license":    []byte(license),
			"hostDomain": []byte(hostDomain),
		},
	}
	serializer := json.NewSerializerWithOptions(json.DefaultMetaFactory, nil, nil,
		json.SerializerOptions{
			Yaml:   true,
			Pretty: true,
			Strict: true,
		},
	)
	err := serializer.Encode(secret, writer)
	if err != nil {
		return err
	}
	return nil
}
