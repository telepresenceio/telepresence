package main

import (
	"fmt"
	"time"

	"github.com/datawire/ambassador/pkg/k8s"
	"github.com/dgrijalva/jwt-go"
	"github.com/pkg/browser"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	k8sTypesMetaV1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sSchema "k8s.io/apimachinery/pkg/runtime/schema"
	k8sClientDynamic "k8s.io/client-go/dynamic"
	k8sClientCoreV1 "k8s.io/client-go/kubernetes/typed/core/v1"
)

const SecretName = "ambassador-internal"
const AmbassadorNamespace = "ambassador"

type LoginClaimsV1 struct {
	LoginTokenVersion  string `json:"login_token_version"`
	jwt.StandardClaims `json:",inline"`
}

func maybeWrongCluster() {
	fmt.Println()
	fmt.Println("Failed to obtain expected information from the cluster.")
	fmt.Println("Is kubectl configured to connect to the correct cluster?")
	fmt.Println()
	fmt.Println("    kubectl -n ambassador get svc,deploy")
	fmt.Println()
}

func aesLogin(_ *cobra.Command, _ []string) error {
	fmt.Println("Connecting to the Ambassador Edge Stack admin UI in this cluster...")

	// Prepare to talk to the cluster
	kubeinfo := k8s.NewKubeInfo("", "", "") // Empty file/ctx/ns for defaults
	restconfig, err := kubeinfo.GetRestConfig()
	if err != nil {
		return errors.Wrap(err, "Failed to connect to cluster")
	}
	coreClient, err := k8sClientCoreV1.NewForConfig(restconfig)
	if err != nil {
		return errors.Wrap(err, "Failed to connect to cluster")
	}
	dynamicClient, err := k8sClientDynamic.NewForConfig(restconfig)
	if err != nil {
		return err
	}

	// Obtain signing key
	// -> kubectl -n $AmbassadorNamespace get secret $SecretName -o json
	secretInterface := coreClient.Secrets(AmbassadorNamespace)
	secret, err := secretInterface.Get(SecretName, k8sTypesMetaV1.GetOptions{})
	if err != nil {
		maybeWrongCluster()
		return err
	}
	// Parse out the private key from the secret
	privatePEM, ok := secret.Data["rsa.key"]
	if !ok {
		maybeWrongCluster()
		return errors.Errorf("secret name=%q namespace=%q exists but does not contain an %q %s field",
			SecretName, AmbassadorNamespace, "rsa.key", "private-key")
	}
	privateKey, err := jwt.ParseRSAPrivateKeyFromPEM(privatePEM)
	if err != nil {
		maybeWrongCluster()
		return errors.Wrap(err, "parse private-key")
	}

	// Figure out the correct hostname
	// -> kubectl -n $AmbassadorNamespace get host -o json
	// and the first host we find for now...
	hostsGetter := dynamicClient.Resource(k8sSchema.GroupVersionResource{
		Group:    "getambassador.io",
		Version:  "v2",
		Resource: "hosts",
	})
	hosts, err := hostsGetter.List(k8sTypesMetaV1.ListOptions{})
	if err != nil {
		maybeWrongCluster()
		return err
	}
	var hostname string
	for _, host := range hosts.Items {
		// FIXME: We should pay attention to the Namespace, maybe some sort of
		// Ambassador ID thing, etc., so we don't pick up the wrong hostname.
		spec, ok := host.Object["spec"].(map[string]interface{})
		if !ok {
			continue
		}
		maybeHostname, ok := spec["hostname"].(string)
		if !ok {
			continue
		}
		hostname = maybeHostname
		break
	}
	if hostname == "" {
		maybeWrongCluster()
		return fmt.Errorf("Did not find a valid Host in your cluster")
	}

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
	url := fmt.Sprintf("https://%s/edge_stack/admin#%s\n", hostname, tokenString)

	if err := browser.OpenURL(url); err != nil {
		fmt.Println("Unexpected error while trying to open your browser.")
		fmt.Println("Visit the following URL to access the Ambassador Edge Stack admin UI:")
		fmt.Println("    ", url)
		return errors.Wrap(err, "browse")
	}
	fmt.Println("Ambassador Edge Stack admin UI has been opened in your browser.")
	return nil
}
