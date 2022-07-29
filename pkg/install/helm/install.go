package helm

import (
	"context"
	"fmt"
	"strings"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/release"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

const helmDriver = "secrets"
const releaseName = "traffic-manager"

func getHelmConfig(ctx context.Context, configFlags *genericclioptions.ConfigFlags, namespace string) (*action.Configuration, error) {
	helmConfig := &action.Configuration{}
	err := helmConfig.Init(configFlags, namespace, helmDriver, func(format string, args ...any) {
		ctx := dlog.WithField(ctx, "source", "helm")
		dlog.Infof(ctx, format, args...)
	})
	if err != nil {
		return nil, err
	}
	return helmConfig, nil
}

func getValues(ctx context.Context) map[string]any {
	clientConfig := client.GetConfig(ctx)
	imgConfig := clientConfig.Images
	imageRegistry := imgConfig.Registry(ctx)
	cloudConfig := clientConfig.Cloud
	imageTag := strings.TrimPrefix(client.Version(), "v")
	values := map[string]any{
		"image": map[string]any{
			"registry": imageRegistry,
			"tag":      imageTag,
		},
		"systemaHost": cloudConfig.SystemaHost,
		"systemaPort": cloudConfig.SystemaPort,
	}
	if !clientConfig.Grpc.MaxReceiveSize.IsZero() {
		values["grpc"] = map[string]any{
			"maxReceiveSize": clientConfig.Grpc.MaxReceiveSize.String(),
		}
	}
	apc := clientConfig.Intercept.AppProtocolStrategy
	if wai, wr := imgConfig.AgentImage(ctx), imgConfig.WebhookRegistry(ctx); wai != "" || wr != "" || apc != k8sapi.Http2Probe {
		agentImage := make(map[string]any)
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
		agentInjector := map[string]any{"agentImage": agentImage}
		values["agentInjector"] = agentInjector
		if apc != k8sapi.Http2Probe {
			agentInjector["appProtocolStrategy"] = apc.String()
		}
	}
	if clientConfig.TelepresenceAPI.Port != 0 {
		values["telepresenceAPI"] = map[string]any{
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

func installNew(ctx context.Context, chrt *chart.Chart, helmConfig *action.Configuration, namespace string, values map[string]any) error {
	dlog.Infof(ctx, "No existing Traffic Manager found in namespace %s, installing %s...", namespace, client.Version())
	install := action.NewInstall(helmConfig)
	install.ReleaseName = releaseName
	install.Namespace = namespace
	install.Atomic = true
	install.CreateNamespace = true
	return timedRun(ctx, func(timeout time.Duration) error {
		install.Timeout = timeout
		_, err := install.Run(chrt, values)
		return err
	})
}

func upgradeExisting(ctx context.Context, existingVer string, chrt *chart.Chart, helmConfig *action.Configuration, namespace string, values map[string]any) error {
	dlog.Infof(ctx, "Existing Traffic Manager %s found in namespace %s, upgrading to %s...", existingVer, namespace, client.Version())
	upgrade := action.NewUpgrade(helmConfig)
	upgrade.Atomic = true
	upgrade.Namespace = namespace
	return timedRun(ctx, func(timeout time.Duration) error {
		upgrade.Timeout = timeout
		_, err := upgrade.Run(releaseName, chrt, values)
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

func IsTrafficManager(ctx context.Context, configFlags *genericclioptions.ConfigFlags, namespace string) (existing *release.Release, helmConfig *action.Configuration, err error) {
	helmConfig, err = getHelmConfig(ctx, configFlags, namespace)
	if err != nil {
		err = fmt.Errorf("failed to initialize helm config: %w", err)
		return
	}

	transitionStart := time.Now()
restart:
	existing, err = getHelmRelease(ctx, helmConfig)
	if err != nil {
		// If we weren't able to get the helm release at all, there's no hope for installing it
		// This could have happened because the user doesn't have the requisite permissions, or because there was some
		// kind of issue communicating with kubernetes. Let's hope it's the former and let's hope the traffic manager
		// is already set up. If it's the latter case (or the traffic manager isn't there), we'll be alerted by
		// a subsequent error anyway.
		dlog.Errorf(ctx, "EnsureTrafficManager(namespace=%q): unable to look for existing helm release: %v. Assuming it's there and continuing...",
			namespace, err)
		err = nil
		return
	}
	if existing == nil {
		dlog.Infof(ctx, "EnsureTrafficManager(namespace=%q): current install: none", namespace)
	} else {
		dlog.Infof(ctx, "EnsureTrafficManager(namespace=%q): current install: version=%q, owner=%q, state.status=%q, state.desc=%q",
			namespace, releaseVer(existing), existing.Config["createdBy"], existing.Info.Status, existing.Info.Description)
		if existing.Info.Status.IsPending() {
			timeout := client.GetConfig(ctx).Timeouts.Get(client.TimeoutHelm)
			if time.Since(transitionStart) > timeout {
				dlog.Infof(ctx, "EnsureTrafficManager(namespace=%q): current install is has been in a pending state for longer than `timeouts.helm` (%v); assuming it's stuck",
					namespace, timeout)
			} else {
				dlog.Infof(ctx, "EnsureTrafficManager(namespace=%q): current install is in a pending state, waiting for it to transition...",
					namespace)
				dtime.SleepWithContext(ctx, 1*time.Second)
				goto restart
			}
		}
	}

	// Under various conditions, helm can leave the release history hanging around after the release is gone.
	// In those cases, an uninstall should clean everything up and leave us ready to install again
	if existing != nil && (existing.Info.Status != release.StatusDeployed) {
		dlog.Infof(ctx, "EnsureTrafficManager(namespace=%q): current status (status=%q, desc=%q) is not %q, so assuming it's corrupt or stuck; removing it...",
			namespace, existing.Info.Status, existing.Info.Description, release.StatusDeployed)
		err = uninstallExisting(ctx, helmConfig, namespace)
		if err != nil {
			err = fmt.Errorf("failed to clean up leftover release history: %w", err)
			return
		}
		existing = nil
	}
	return existing, helmConfig, err
}

// EnsureTrafficManager ensures the traffic manager is installed
func EnsureTrafficManager(ctx context.Context, configFlags *genericclioptions.ConfigFlags, namespace string, helmInfo *connector.HelmInfo) error {
	existing, helmConfig, err := IsTrafficManager(ctx, configFlags, namespace)
	if err != nil {
		return fmt.Errorf("err detecting traffic manager: %w", err)
	}

	chrt, err := loadChart()
	if err != nil {
		return fmt.Errorf("unable to load built-in helm chart: %w", err)
	}

	// OK, now install things.
	values := getValues(ctx)

	for _, path := range helmInfo.ValuePaths {
		vals, err := chartutil.ReadValuesFile(path)
		if err != nil {
			return fmt.Errorf("--values path %q not readable: %v", vals, err)
		}

		values = chartutil.CoalesceTables(vals.AsMap(), values)
	}

	if existing == nil { // fresh install
		dlog.Infof(ctx, "EnsureTrafficManager(namespace=%q): performing fresh install...", namespace)
		err = installNew(ctx, chrt, helmConfig, namespace, values)
		if err != nil {
			return err
		}
	} else { // replace existing install
		if helmInfo.Upgrade {
			dlog.Infof(ctx, "EnsureTrafficManager(namespace=%q): replacing Traffic Manager from %q to %q...",
				namespace, releaseVer(existing), strings.TrimPrefix(client.Version(), "v"))
			if err := upgradeExisting(ctx, releaseVer(existing), chrt, helmConfig, namespace, values); err != nil {
				return err
			}
		} else {
			return fmt.Errorf("traffic manager version %q is already installed, use the '--upgrade' flag to replace it", releaseVer(existing))
		}
	}

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
	return uninstallExisting(ctx, helmConfig, namespace)
}
