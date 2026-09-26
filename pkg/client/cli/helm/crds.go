package helm

import (
	"context"
	"fmt"

	"helm.sh/helm/v3/pkg/chart"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apiextensionsv1client "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"
)

// crdName extracts metadata.name from a CRD manifest's JSON encoding.
func crdName(data []byte) (string, error) {
	var obj struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	}
	if err := yaml.Unmarshal(data, &obj); err != nil {
		return "", err
	}
	if obj.Metadata.Name == "" {
		return "", fmt.Errorf("CRD manifest has no metadata.name")
	}
	return obj.Metadata.Name, nil
}

// applyCRDs server-side applies every CRD object of chrt using restConfig,
// so that a CustomResourceDefinition already owned by another release is
// updated in place rather than rejected by Helm's release-ownership check.
func applyCRDs(ctx context.Context, restConfig *rest.Config, chrt *chart.Chart) error {
	client, err := apiextensionsclientset.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("unable to create apiextensions client: %w", err)
	}
	return applyCRDObjects(ctx, client.ApiextensionsV1(), chrt)
}

func applyCRDObjects(ctx context.Context, client apiextensionsv1client.ApiextensionsV1Interface, chrt *chart.Chart) error {
	for _, crd := range chrt.CRDObjects() {
		js, err := yaml.YAMLToJSON(crd.File.Data)
		if err != nil {
			return fmt.Errorf("unable to parse CRD %s: %w", crd.Name, err)
		}
		name, err := crdName(js)
		if err != nil {
			return fmt.Errorf("unable to parse CRD %s: %w", crd.Name, err)
		}
		_, err = client.CustomResourceDefinitions().Patch(
			ctx, name, types.ApplyPatchType, js, metav1.PatchOptions{
				FieldManager: "telepresence",
				Force:        ptr.To(true),
			})
		if err != nil {
			return fmt.Errorf("unable to apply CRD %s: %w", name, err)
		}
	}
	return nil
}
