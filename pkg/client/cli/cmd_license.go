package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
)

func LicenseCommand() *cobra.Command {
	var flags struct {
		id          string
		outputFile  string
		licenseFile string
		hostDomain  string
	}
	cmd := &cobra.Command{
		Use:  "license [flags]",
		Args: cobra.NoArgs,

		Short: "Get License from Ambassador Cloud",
		Long: `Get License from Ambassador Cloud. For more information on what
licenses are used for, head to:
https://www.getambassador.io/docs/telepresence/latest/reference/cluster-config/`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return getCloudLicense(cmd.Context(), cmd.OutOrStdout(),
				flags.id, flags.outputFile, flags.licenseFile, flags.hostDomain)
		},
	}

	cmd.Flags().StringVarP(&flags.id, "id", "i", "", "The id associated with your license.")

	cmd.Flags().StringVarP(&flags.outputFile, "output-file", "o", "", "The file where you want the license secret to be output to")
	cmd.Flags().StringVarP(&flags.licenseFile, "license-file", "f", "", "The file containing your license if you've downloaded it already")
	cmd.Flags().StringVarP(&flags.hostDomain, "host-domain", "d", "auth.datawire.io", "The host domain providing your license")
	return cmd
}

// getCloudLicense communicates with system a, acquires the jwt formatted license
// given by the id, places it in a secret, and outputs it to stdout or writes it
// to a file given by the user
func getCloudLicense(ctx context.Context, stdout io.Writer, id, outputFile, licenseFile, hostDomain string) error {
	if licenseFile == "" && id == "" {
		return errors.New("Must use either --id or --license-file flag")
	}
	var license string
	var err error
	if licenseFile == "" {
		// If we are getting the license from the cloud, we override the hostDomain
		// since the function will provide which host the license came from
		license, hostDomain, err = cliutil.GetCloudLicense(ctx, outputFile, id)
		if err != nil {
			return err
		}
	} else {
		contents, err := os.ReadFile(licenseFile)
		if err != nil {
			return err
		}
		// We don't want a pesky trailing \n to mess up the jwt
		// so we remove one if it exists
		license = string(contents)
		license = strings.TrimSuffix(license, "\n")
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
