package helm

import (
	"context"
	"encoding/json"
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
	"github.com/datawire/k8sapi/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

const (
	helmDriver                = "secrets"
	trafficManagerReleaseName = "traffic-manager"
	crdReleaseName            = "telepresence-crds"
)

var GetValuesFunc = GetValues //nolint:gochecknoglobals // extension point

type RequestType int32

const (
	Install RequestType = iota
	Upgrade
	Uninstall
)

type Request struct {
	Type        RequestType
	ValuesJson  []byte
	ReuseValues bool
	ResetValues bool
	Crds        bool
	NoHooks     bool
}

func getHelmConfig(ctx context.Context, clientGetter genericclioptions.RESTClientGetter, namespace string) (*action.Configuration, error) {
	helmConfig := &action.Configuration{}
	err := helmConfig.Init(clientGetter, namespace, helmDriver, func(format string, args ...any) {
		ctx := dlog.WithField(ctx, "source", "helm")
		dlog.Infof(ctx, format, args...)
	})
	if err != nil {
		return nil, err
	}
	return helmConfig, nil
}

func GetValues(ctx context.Context) map[string]any {
	clientConfig := client.GetConfig(ctx)
	imgConfig := clientConfig.Images()
	imageRegistry := imgConfig.Registry(ctx)
	imageTag := strings.TrimPrefix(client.Version(), "v")
	values := map[string]any{
		"image": map[string]any{
			"registry": imageRegistry,
			"tag":      imageTag,
		},
	}
	if !clientConfig.Grpc().MaxReceiveSizeV.IsZero() {
		values["grpc"] = map[string]any{
			"maxReceiveSize": clientConfig.Grpc().MaxReceiveSizeV.String(),
		}
	}
	if wai, wr := imgConfig.AgentImage(ctx), imgConfig.WebhookRegistry(ctx); wai != "" || wr != "" {
		image := make(map[string]any)
		if wai != "" {
			i := strings.LastIndexByte(wai, '/')
			if i >= 0 {
				if wr == "" {
					wr = wai[:i]
				}
				wai = wai[i+1:]
			}
			parts := strings.Split(wai, ":")
			name := wai
			tag := ""
			if len(parts) > 1 {
				name = parts[0]
				tag = parts[1]
			}
			image["name"] = name
			image["tag"] = tag
		}
		if wr != "" {
			image["registry"] = wr
		}
		values["agent"] = map[string]any{"image": image}
	}

	if apc := clientConfig.Intercept().AppProtocolStrategy; apc != k8sapi.Http2Probe {
		values["agentInjector"] = map[string]any{"appProtocolStrategy": apc.String()}
	}
	if clientConfig.TelepresenceAPI().Port != 0 {
		values["telepresenceAPI"] = map[string]any{
			"port": clientConfig.TelepresenceAPI().Port,
		}
	}

	return values
}

func timedRun(ctx context.Context, run func(time.Duration) error) error {
	timeouts := client.GetConfig(ctx).Timeouts()
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

func installNew(
	ctx context.Context,
	chrt *chart.Chart,
	helmConfig *action.Configuration,
	releaseName, namespace string,
	req *Request,
	values map[string]any,
) error {
	dlog.Infof(ctx, "No existing %s found in namespace %s, installing %s...", releaseName, namespace, client.Version())
	install := action.NewInstall(helmConfig)
	install.ReleaseName = releaseName
	install.Namespace = namespace
	install.Atomic = true
	install.CreateNamespace = true
	install.DisableHooks = req.NoHooks
	return timedRun(ctx, func(timeout time.Duration) error {
		install.Timeout = timeout
		_, err := install.Run(chrt, values)
		return err
	})
}

func upgradeExisting(
	ctx context.Context,
	existingVer string,
	chrt *chart.Chart,
	helmConfig *action.Configuration,
	releaseName, ns string,
	req *Request,
	values map[string]any,
) error {
	dlog.Infof(ctx, "Existing Traffic Manager %s found in namespace %s, upgrading to %s...", existingVer, ns, client.Version())
	upgrade := action.NewUpgrade(helmConfig)
	upgrade.Atomic = true
	upgrade.Namespace = ns
	upgrade.ResetValues = req.ResetValues
	upgrade.ReuseValues = req.ReuseValues
	upgrade.DisableHooks = req.NoHooks
	return timedRun(ctx, func(timeout time.Duration) error {
		upgrade.Timeout = timeout
		_, err := upgrade.Run(releaseName, chrt, values)
		return err
	})
}

func uninstallExisting(ctx context.Context, helmConfig *action.Configuration, releaseName, namespace string, req *Request) error {
	dlog.Infof(ctx, "Uninstalling %s in namespace %s", releaseName, namespace)
	uninstall := action.NewUninstall(helmConfig)
	uninstall.DisableHooks = req.NoHooks
	return timedRun(ctx, func(timeout time.Duration) error {
		uninstall.Timeout = timeout
		_, err := uninstall.Run(releaseName)
		return err
	})
}

func isInstalled(ctx context.Context, clientGetter genericclioptions.RESTClientGetter, releaseName, namespace string) (*release.Release, *action.Configuration, error) {
	dlog.Debug(ctx, "getHelmConfig")
	helmConfig, err := getHelmConfig(ctx, clientGetter, namespace)
	if err != nil {
		err = fmt.Errorf("failed to initialize helm config: %w", err)
		return nil, nil, err
	}

	var existing *release.Release
	transitionStart := time.Now()
	timeout := client.GetConfig(ctx).Timeouts().Get(client.TimeoutHelm)
	for time.Since(transitionStart) < timeout {
		dlog.Debugf(ctx, "getHelmRelease")
		if existing, err = getHelmRelease(ctx, releaseName, helmConfig); err != nil {
			// If we weren't able to get the helm release at all, there's no hope for installing it
			// This could have happened because the user doesn't have the requisite permissions, or because there was some
			// kind of issue communicating with kubernetes. Let's hope it's the former and let's hope the traffic manager
			// is already set up. If it's the latter case (or the traffic manager isn't there), we'll be alerted by
			// a subsequent error anyway.
			return nil, nil, err
		}
		if existing == nil {
			dlog.Infof(ctx, "isInstalled(namespace=%q): current install: none", namespace)
			return nil, helmConfig, nil
		}
		st := existing.Info.Status
		if !(st.IsPending() || st == release.StatusUninstalling) {
			owner := "unknown"
			if ow, ok := existing.Config["createdBy"]; ok {
				owner = ow.(string)
			}
			dlog.Infof(ctx, "isInstalled(namespace=%q): current install: version=%q, owner=%q, state.status=%q, state.desc=%q",
				namespace, releaseVer(existing), owner, st, existing.Info.Description)
			return existing, helmConfig, nil
		}
		dlog.Infof(ctx, "isInstalled(namespace=%q): current install is in a pending or uninstalling state, waiting for it to transition...",
			namespace)
		dtime.SleepWithContext(ctx, 1*time.Second)
	}
	dlog.Infof(ctx, "isInstalled(namespace=%q): current install is has been in a pending state for longer than `timeouts.helm` (%v); assuming it's stuck",
		namespace, timeout)
	return existing, helmConfig, nil
}

func EnsureTrafficManager(ctx context.Context, clientGetter genericclioptions.RESTClientGetter, namespace string, req *Request) error {
	if req.Crds {
		dlog.Debug(ctx, "loading build-in helm chart")
		crdChart, err := loadCRDChart()
		if err != nil {
			return fmt.Errorf("unable to load built-in helm chart: %w", err)
		}

		err = ensureIsInstalled(ctx, clientGetter, crdChart, crdReleaseName, namespace, req)
		if err != nil {
			return fmt.Errorf("failed to install traffic manager CRDs: %w", err)
		}
		return nil
	}

	coreChart, err := loadCoreChart()
	if err != nil {
		return fmt.Errorf("unable to load built-in helm chart: %w", err)
	}

	err = ensureIsInstalled(ctx, clientGetter, coreChart, trafficManagerReleaseName, namespace, req)
	if err != nil {
		return fmt.Errorf("failed to install traffic manager: %w", err)
	}

	return nil
}

// EnsureTrafficManager ensures the traffic manager is installed.
func ensureIsInstalled(
	ctx context.Context, clientGetter genericclioptions.RESTClientGetter, chrt *chart.Chart,
	releaseName, namespace string, req *Request,
) error {
	existing, helmConfig, err := isInstalled(ctx, clientGetter, releaseName, namespace)
	if err != nil {
		return fmt.Errorf("err detecting %s: %w", releaseName, err)
	}

	// Under various conditions, helm can leave the release history hanging around after the release is gone.
	// In those cases, an uninstall should clean everything up and leave us ready to install again
	if existing != nil && (existing.Info.Status != release.StatusDeployed) {
		dlog.Infof(ctx, "ensureIsInstalled(namespace=%q): current status (status=%q, desc=%q) is not %q, so assuming it's corrupt or stuck; removing it...",
			namespace, existing.Info.Status, existing.Info.Description, release.StatusDeployed)
		urq := Request{
			Type:    Uninstall,
			NoHooks: true,
		}
		err = uninstallExisting(ctx, helmConfig, namespace, releaseName, &urq)
		if err != nil {
			return fmt.Errorf("failed to clean up leftover release history: %w", err)
		}
		existing = nil
	}

	// OK, now install things.
	var providedVals map[string]any
	if len(req.ValuesJson) > 0 {
		if err := json.Unmarshal(req.ValuesJson, &providedVals); err != nil {
			return fmt.Errorf("unable to parse values JSON: %w", err)
		}
	}

	var vals map[string]any
	if len(providedVals) > 0 {
		vals = chartutil.CoalesceTables(providedVals, GetValuesFunc(ctx))
	} else {
		// No values were provided. This means that an upgrade should retain existing values unless
		// reset-values is true.
		if req.Type == Upgrade && !req.ResetValues {
			req.ReuseValues = true
		}
		vals = GetValuesFunc(ctx)
	}

	switch {
	case existing == nil: // fresh install
		// Only the traffic manager release has a legacy version.
		if releaseName == trafficManagerReleaseName {
			dlog.Debugf(ctx, "Importing legacy for namespace %s", namespace)
			if err := importLegacy(ctx, releaseName, namespace); err != nil {
				// Similarly to the error check for getHelmRelease, this could happen because of missing permissions,
				// or a different k8s error. We don't want to block on permissions failures, so let's log and hope.
				dlog.Errorf(ctx, "ensureIsInstalled(namespace=%q): unable to import existing k8s resources: %v. Assuming traffic-manager is setup and continuing...",
					namespace, err)
				return nil
			}
		}

		dlog.Infof(ctx, "ensureIsInstalled(namespace=%q): performing fresh install...", namespace)
		err = installNew(ctx, chrt, helmConfig, releaseName, namespace, req, vals)
	case req.Type == Upgrade: // replace existing install
		dlog.Infof(ctx, "ensureIsInstalled(namespace=%q): replacing %s from %q to %q...",
			namespace, releaseName, releaseVer(existing), strings.TrimPrefix(client.Version(), "v"))
		err = upgradeExisting(ctx, releaseVer(existing), chrt, helmConfig, releaseName, namespace, req, vals)
	default:
		err = errcat.User.Newf(
			"%s version %q is already installed, use 'telepresence helm upgrade' instead to replace it",
			releaseName, releaseVer(existing))
	}
	return err
}

// DeleteTrafficManager deletes the traffic manager.
func DeleteTrafficManager(
	ctx context.Context, clientGetter genericclioptions.RESTClientGetter, namespace string, errOnFail bool, req *Request,
) error {
	if !req.Crds {
		err := ensureIsDeleted(ctx, clientGetter, trafficManagerReleaseName, namespace, errOnFail, req)
		if err != nil {
			return err
		}
		return nil
	}

	err := ensureIsDeleted(ctx, clientGetter, crdReleaseName, namespace, errOnFail, req)
	if err != nil {
		return err
	}

	return nil
}

func ensureIsDeleted(
	ctx context.Context,
	clientGetter genericclioptions.RESTClientGetter,
	releaseName, namespace string,
	errOnFail bool,
	req *Request,
) error {
	helmConfig, err := getHelmConfig(ctx, clientGetter, namespace)
	if err != nil {
		return fmt.Errorf("failed to initialize helm config: %w", err)
	}

	existing, err := getHelmRelease(ctx, releaseName, helmConfig)
	if err != nil {
		err := fmt.Errorf("unable to look for existing helm release in namespace %s: %w", namespace, err)
		if errOnFail {
			return err
		}
		dlog.Infof(ctx, "%s. Assuming it's already gone...", err.Error())
		return nil
	}
	if existing == nil {
		err := fmt.Errorf("%s in namespace %s already deleted", releaseName, namespace)
		if errOnFail {
			return err
		}
		dlog.Info(ctx, err.Error())
		return nil
	}
	return uninstallExisting(ctx, helmConfig, releaseName, namespace, req)
}
