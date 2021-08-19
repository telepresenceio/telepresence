package helm

import (
	"context"
	"fmt"
	"strings"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/datawire/dlib/dlog"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	cl "github.com/telepresenceio/telepresence/v2/pkg/client"
)

const helmDriver = "secrets"
const releaseName = "traffic-manager"
const releaseOwner = "telepresence-cli"

func getHelmConfig(ctx context.Context, configFlags *kates.ConfigFlags, namespace string) (*action.Configuration, error) {
	helmConfig := &action.Configuration{}
	err := helmConfig.Init(configFlags, namespace, helmDriver, func(format string, args ...interface{}) {
		ctx := dlog.WithField(ctx, "source", "helm")
		dlog.Debugf(ctx, format, args...)
	})
	if err != nil {
		return nil, err
	}
	return helmConfig, nil
}

func getValues(ctx context.Context) map[string]interface{} {
	clientConfig := client.GetConfig(ctx)
	imgConfig := clientConfig.Images
	imageRegistry := imgConfig.Registry
	imageTag := strings.TrimPrefix(client.Version(), "v")
	values := map[string]interface{}{
		"image": map[string]interface{}{
			"registry": imageRegistry,
			"tag":      imageTag,
		},
		"createdBy": releaseOwner,
	}
	if mxRecvSize := clientConfig.Grpc.MaxReceiveSize; mxRecvSize != nil {
		values["grpc"] = map[string]interface{}{
			"maxReceiveSize": mxRecvSize.String(),
		}
	}
	if imgConfig.WebhookAgentImage != "" {
		parts := strings.Split(imgConfig.WebhookAgentImage, ":")
		image := imgConfig.WebhookAgentImage
		tag := ""
		if len(parts) > 1 {
			image = parts[0]
			tag = parts[1]
		}
		values["agentInjector"] = map[string]interface{}{
			"agentImage": map[string]interface{}{
				"registry": imgConfig.WebhookRegistry,
				"name":     image,
				"tag":      tag,
			},
		}
	}

	return values
}

func installNew(ctx context.Context, chrt *chart.Chart, helmConfig *action.Configuration, namespace string) error {
	dlog.Info(ctx, "No existing Traffic Manager found, installing...")
	install := action.NewInstall(helmConfig)
	install.ReleaseName = releaseName
	install.Namespace = namespace
	install.Timeout = client.GetConfig(ctx).Timeouts.Helm
	install.Atomic = true
	install.CreateNamespace = true
	_, err := install.Run(chrt, getValues(ctx))
	return err
}

func upgradeExisting(ctx context.Context, chrt *chart.Chart, helmConfig *action.Configuration, namespace string) error {
	dlog.Info(ctx, "Existing Traffic Manager found, upgrading...")
	upgrade := action.NewUpgrade(helmConfig)
	upgrade.Timeout = client.GetConfig(ctx).Timeouts.Helm
	upgrade.Atomic = true
	upgrade.Namespace = namespace
	_, err := upgrade.Run(releaseName, chrt, getValues(ctx))
	return err
}

// EnsureTrafficManager ensures the traffic manager is installed
func EnsureTrafficManager(ctx context.Context, configFlags *kates.ConfigFlags, client *kates.Client, namespace string, env *cl.Env) error {
	helmConfig, err := getHelmConfig(ctx, configFlags, namespace)
	if err != nil {
		return fmt.Errorf("failed to initialize helm config: %w", err)
	}
	existing, err := getHelmRelease(ctx, helmConfig)
	if err != nil {
		// If we weren't able to get the helm release at all, there's no hope for installing it
		// This could have happened because the user doesn't have the requisite permissions, or because there was some
		// kind of issue communicating with kubernetes. Let's hope it's the former and let's hope the traffic manager
		// is already set up. If it's the latter case (or the traffic manager isn't there), we'll be alerted by
		// a subsequent error anyway.
		dlog.Errorf(ctx, "Unable to look for existing helm release: %v. Assuming it's there and continuing...", err)
		return nil
	}

	chrt, err := loadChart()
	if err != nil {
		return fmt.Errorf("unable to load built-in helm chart: %w", err)
	}
	if existing == nil {
		err := importLegacy(ctx, namespace, client)
		if err != nil {
			// Similarly to the error check for getHelmRelease, this could happen because of missing permissions,
			// or a different k8s error. We don't want to block on permissions failures, so let's log and hope.
			dlog.Errorf(ctx, "Unable to import existing k8s resources: %v. Assuming traffic-manager is setup and continuing...", err)
			return nil
		}

		err = installNew(ctx, chrt, helmConfig, namespace)
		if err != nil {
			return err
		}
		// We've just modified the resources totally outside of the kates client, so invalidate the cache to make sure
		// it'll return fresh resources
		client.InvalidateCache()
		return nil
	}
	if shouldManageRelease(ctx, existing) && shouldUpgradeRelease(ctx, existing) {
		err = upgradeExisting(ctx, chrt, helmConfig, namespace)
		if err != nil {
			return err
		}
		client.InvalidateCache()
		return nil
	}
	dlog.Info(ctx, "Existing Traffic Manager not owned by cli or does not need upgrade, will not modify")
	return nil
}

// DeleteTrafficManager deletes the traffic manager
func DeleteTrafficManager(ctx context.Context, configFlags *kates.ConfigFlags, namespace string, env *cl.Env) error {
	helmConfig, err := getHelmConfig(ctx, configFlags, namespace)
	if err != nil {
		return fmt.Errorf("failed to initialize helm config: %w", err)
	}
	existing, err := getHelmRelease(ctx, helmConfig)
	if err != nil {
		dlog.Errorf(ctx, "Unable to look for existing helm release: %v. Assuming it's already gone...", err)
		return nil
	}
	if existing == nil || !shouldManageRelease(ctx, existing) {
		dlog.Info(ctx, "Traffic Manager already deleted or not owned by cli, will not uninstall")
		return nil
	}
	dlog.Info(ctx, "Uninstalling Traffic Manager")
	uninstall := action.NewUninstall(helmConfig)
	uninstall.Timeout = client.GetConfig(ctx).Timeouts.Helm
	_, err = uninstall.Run(releaseName)
	return err
}
