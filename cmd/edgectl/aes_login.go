package main

import (
	"crypto/rsa"
	"fmt"
	"time"

	"github.com/datawire/ambassador/pkg/k8s"
	"github.com/dgrijalva/jwt-go"
	"github.com/pkg/browser"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	k8sTypesMetaV1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sClientCoreV1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
)

const SecretName = "ambassador-internal"

type LoginClaimsV1 struct {
	LoginTokenVersion  string `json:"login_token_version"`
	jwt.StandardClaims `json:",inline"`
}

func aesLogin(cmd *cobra.Command, args []string) error {
	fmt.Println("Connecting to the Ambassador Edge Stack admin UI in this cluster...")

	// Prepare to talk to the cluster
	context, _ := cmd.Flags().GetString("context")
	namespace, _ := cmd.Flags().GetString("namespace")
	kubeinfo := k8s.NewKubeInfo("", context, namespace) // Default namespace here
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

	// Figure out the correct hostname
	hostname := args[0]
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
	url := fmt.Sprintf("https://%s/edge_stack/admin/#%s", hostname, tokenString)

	if err := browser.OpenURL(url); err != nil {
		fmt.Println("Unexpected error while trying to open your browser.")
		fmt.Println("Visit the following URL to access the Ambassador Edge Stack admin UI:")
		fmt.Println("    ", url)
		return errors.Wrap(err, "browse")
	}
	fmt.Println("Ambassador Edge Stack admin UI has been opened in your browser.")
	return nil
}

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
