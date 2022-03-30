package helm

import (
	"context"
	"fmt"
	"strings"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

const helmDriver = "secrets"
const releaseName = "traffic-manager"
const releaseOwner = "telepresence-cli"

func getHelmConfig(ctx context.Context, configFlags *genericclioptions.ConfigFlags, namespace string) (*action.Configuration, error) {
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
	imageRegistry := imgConfig.Registry(ctx)
	cloudConfig := clientConfig.Cloud
	imageTag := strings.TrimPrefix(client.Version(), "v")
	values := map[string]interface{}{
		"image": map[string]interface{}{
			"registry": imageRegistry,
			"tag":      imageTag,
		},
		"systemaHost": cloudConfig.SystemaHost,
		"systemaPort": cloudConfig.SystemaPort,
		"createdBy":   releaseOwner,
	}
	if !clientConfig.Grpc.MaxReceiveSize.IsZero() {
		values["grpc"] = map[string]interface{}{
			"maxReceiveSize": clientConfig.Grpc.MaxReceiveSize.String(),
		}
	}
	apc := clientConfig.Intercept.AppProtocolStrategy
	if wai, wr := imgConfig.WebhookAgentImage(ctx), imgConfig.WebhookRegistry(ctx); wai != "" || wr != "" || apc != k8sapi.Http2Probe {
		agentImage := make(map[string]interface{})
		if wai != "" {
			parts := strings.Split(wai, ":")
			image := wai
			tag := ""
			if len(parts) > 1 {
				image = parts[0]
				tag = parts[1]
			}
			agentImage["name"] = image
			agentImage["tag"] = tag
		}
		if wr != "" {
			agentImage["registry"] = wr
		}
		agentInjector := map[string]interface{}{"agentImage": agentImage}
		values["agentInjector"] = agentInjector
		if apc != k8sapi.Http2Probe {
			agentInjector["appProtocolStrategy"] = apc.String()
		}
	}
	if clientConfig.TelepresenceAPI.Port != 0 {
		values["telepresenceAPI"] = map[string]interface{}{
			"port": clientConfig.TelepresenceAPI.Port,
		}
	}
	return values
}

func timedRun(ctx context.Context, run func(time.Duration) error) error {
	timeouts := client.GetConfig(ctx).Timeouts
	ctx, cancel := timeouts.TimeoutContext(ctx, client.TimeoutHelm)
	defer cancel()

	runResult := make(chan error)
	go func() {
		runResult <- run(timeouts.Get(client.TimeoutHelm))
	}()

	select {
	case <-ctx.Done():
		return client.CheckTimeout(ctx, ctx.Err())
	case err := <-runResult:
		if err != nil {
			err = client.CheckTimeout(ctx, err)
		}
		return err
	}
}

func installNew(ctx context.Context, chrt *chart.Chart, helmConfig *action.Configuration, namespace string) error {
	dlog.Infof(ctx, "No existing Traffic Manager found in namespace %s, installing %s...", namespace, client.Version())
	install := action.NewInstall(helmConfig)
	install.ReleaseName = releaseName
	install.Namespace = namespace
	install.Atomic = true
	install.CreateNamespace = true
	return timedRun(ctx, func(timeout time.Duration) error {
		install.Timeout = timeout
		_, err := install.Run(chrt, getValues(ctx))
		return err
	})
}

func upgradeExisting(ctx context.Context, existingVer string, chrt *chart.Chart, helmConfig *action.Configuration, namespace string) error {
	dlog.Infof(ctx, "Existing Traffic Manager %s found in namespace %s, upgrading to %s...", existingVer, namespace, client.Version())
	upgrade := action.NewUpgrade(helmConfig)
	upgrade.Atomic = true
	upgrade.Namespace = namespace
	return timedRun(ctx, func(timeout time.Duration) error {
		upgrade.Timeout = timeout
		_, err := upgrade.Run(releaseName, chrt, getValues(ctx))
		return err
	})
}

func uninstallExisting(ctx context.Context, helmConfig *action.Configuration, namespace string) error {
	dlog.Infof(ctx, "Uninstalling Traffic Manager in namespace %s", namespace)
	uninstall := action.NewUninstall(helmConfig)
	return timedRun(ctx, func(timeout time.Duration) error {
		uninstall.Timeout = timeout
		_, err := uninstall.Run(releaseName)
		return err
	})
}

// EnsureTrafficManager ensures the traffic manager is installed
func EnsureTrafficManager(ctx context.Context, configFlags *genericclioptions.ConfigFlags, namespace string) error {
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
	// Under various conditions, helm can leave the release history hanging around after the release is gone.
	// In those cases, an uninstall should clean everything up and leave us ready to install again
	if existing != nil && shouldManageRelease(ctx, existing) && releaseNeedsCleanup(ctx, existing) {
		err := uninstallExisting(ctx, helmConfig, namespace)
		if err != nil {
			return fmt.Errorf("failed to clean up leftover release history: %w", err)
		}
		existing = nil
	}
	if existing == nil {
		err := importLegacy(ctx, namespace)
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
		return nil
	}
	ver := releaseVer(existing)
	if shouldManageRelease(ctx, existing) && shouldUpgradeRelease(ctx, existing) {
		err = upgradeExisting(ctx, ver, chrt, helmConfig, namespace)
		if err != nil {
			return err
		}
		return nil
	}
	dlog.Infof(ctx, "Existing Traffic Manager %s not owned by cli or does not need upgrade, will not modify", ver)
	return nil
}

// DeleteTrafficManager deletes the traffic manager
func DeleteTrafficManager(ctx context.Context, configFlags *genericclioptions.ConfigFlags, namespace string, errOnFail bool) error {
	helmConfig, err := getHelmConfig(ctx, configFlags, namespace)
	if err != nil {
		return fmt.Errorf("failed to initialize helm config: %w", err)
	}
	existing, err := getHelmRelease(ctx, helmConfig)
	if err != nil {
		err := fmt.Errorf("unable to look for existing helm release in namespace %s: %w", namespace, err)
		if errOnFail {
			return err
		}
		dlog.Infof(ctx, "%s. Assuming it's already gone...", err.Error())
		return nil
	}
	if existing == nil {
		err := fmt.Errorf("traffic Manager in namespace %s already deleted", namespace)
		if errOnFail {
			return err
		}
		dlog.Info(ctx, err.Error())
		return nil
	}
	if !shouldManageRelease(ctx, existing) {
		err := fmt.Errorf("traffic Manager in namespace %s is not owned by cli, will not uninstall", namespace)
		if errOnFail {
			return err
		}
		dlog.Info(ctx, err.Error())
		return nil
	}
	return uninstallExisting(ctx, helmConfig, namespace)
}
