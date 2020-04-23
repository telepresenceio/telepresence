package edgectl

import (
	"crypto/rsa"
	"fmt"
	"time"

	"github.com/pkg/browser"

	"github.com/dgrijalva/jwt-go"
	"github.com/gookit/color"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	k8sTypesMetaV1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sClientCoreV1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"

	"github.com/datawire/ambassador/pkg/k8s"
)

const SecretName = "ambassador-internal"

type LoginClaimsV1 struct {
	LoginTokenVersion string `json:"login_token_version"`
	jwt.StandardClaims
}

func AESLogin(cmd *cobra.Command, args []string) error {
	fmt.Println(color.Info.Sprintf("Connecting to the Ambassador Edge Policy Console in this cluster..."))

	// Grab options
	context, _ := cmd.Flags().GetString("context")
	namespace, _ := cmd.Flags().GetString("namespace")
	justShowURL, _ := cmd.Flags().GetBool("url")
	showToken, _ := cmd.Flags().GetBool("token")

	// Figure out the correct hostname
	hostname := args[0]

	// Prepare to talk to the cluster
	kubeinfo := k8s.NewKubeInfo("", context, namespace) // Default namespace is "ambassador"

	return DoLogin(kubeinfo, context, namespace, hostname, !justShowURL, justShowURL, showToken, false)
}

func DoLogin(kubeinfo *k8s.KubeInfo, context, namespace, hostname string, openInBrowser, showURL, showToken, showWelcome bool) error {
	restconfig, err := kubeinfo.GetRestConfig()
	if err != nil {
		return errors.Wrap(err, "Failed to connect to cluster (rest)")
	}

	// Obtain signing key
	// -> kubectl -n $namespace get secret $SecretName -o json
	privateKey, err := getSigningKey(restconfig, namespace)
	if err != nil {
		fmt.Println()
		fmt.Println("Failed to obtain expected information from the cluster.")
		fmt.Println("Is kubectl configured to connect to the correct cluster?")
		fmt.Printf("Is %s the namespace where Ambassador is installed?\n\n", namespace)
		fmt.Printf("    kubectl -n %s get svc,deploy\n\n", namespace)
		return err
	}

	// FIXME: validate that hostname by querying
	// https://{{hostname}}/edge_stack/admin/api/ambassador_cluster_id and
	// verifying that it returns the same UUID via direct access and via
	// port-forward/teleproxy. This avoids leaking login credentials to the
	// operator of a different website.

	// Construct claims
	now := time.Now()
	duration := 30 * time.Minute
	claims := &LoginClaimsV1{
		"v1",
		jwt.StandardClaims{
			IssuedAt:  now.Unix(),
			NotBefore: now.Unix(),
			ExpiresAt: (now.Add(duration)).Unix(),
		},
	}

	// Generate JWT
	token := jwt.NewWithClaims(jwt.GetSigningMethod("PS512"), claims)
	tokenString, err := token.SignedString(privateKey)
	if err != nil {
		return errors.Wrap(err, "Unexpected error generating JWT")
	}

	// Output
	url := ""
	if showWelcome {
		url = fmt.Sprintf("https://%s/edge_stack/admin/?welcome=true#%s", hostname, tokenString)
	} else {
		url = fmt.Sprintf("https://%s/edge_stack/admin/#%s", hostname, tokenString)
	}

	// Remember if the browser successfully opened the URL
	browserOpened := false

	if openInBrowser {
		err = browser.OpenURL(url)
		if err == nil {
			browserOpened = true
		} else {
			fmt.Println("Unexpected error while trying to open your browser.")
			err = errors.Wrap(err, "browse")
		}
	}

	if showURL {
		if browserOpened {
			fmt.Println("We've opened the Ambassador Edge Policy Console for you in your browser:")
		} else {
			fmt.Println("Visit the following URL to access the Ambassador Edge Policy Console:")
		}

		fmt.Println(url)

		// Whitespace if we are also showing the token or opening in the browser
		if showToken {
			fmt.Println()
		}
	}

	if showToken {
		fmt.Println("The login token is")
		fmt.Println("    ", tokenString)
	}

	return err
}

// getSigningKey retrieves the designated secret from the cluster and returns
// the private key extracted from the secret data
func getSigningKey(restconfig *rest.Config, namespace string) (*rsa.PrivateKey, error) {
	coreClient, err := k8sClientCoreV1.NewForConfig(restconfig)
	if err != nil {
		return nil, errors.Wrap(err, "Failed to connect to cluster (core)")
	}
	secretInterface := coreClient.Secrets(namespace)
	secret, err := secretInterface.Get(SecretName, k8sTypesMetaV1.GetOptions{})
	if err != nil {
		return nil, err
	}
	// Parse out the private key from the secret
	privatePEM, ok := secret.Data["rsa.key"]
	if !ok {
		return nil, errors.Errorf("secret name=%q namespace=%q exists but does not contain an %q %s field",
			SecretName, namespace, "rsa.key", "private-key")
	}
	return jwt.ParseRSAPrivateKeyFromPEM(privatePEM)
}
