package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	argorollouts "github.com/datawire/argo-rollouts-go-client/pkg/apis/rollouts/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept"
	api "github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/api/v1alpha1"
	"github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/controller"
	"github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/kafkaconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/runtimeconfig"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: telepresence-kafka controller|splitter")
	}
	switch args[0] {
	case "controller":
		return runController(args[1:])
	case "splitter":
		return runSplitter(args[1:])
	default:
		return fmt.Errorf("unknown telepresence-kafka command %q", args[0])
	}
}

func newScheme() (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		clientgoscheme.AddToScheme,
		appsv1.AddToScheme,
		corev1.AddToScheme,
		argorollouts.AddToScheme,
		api.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			return nil, err
		}
	}
	return scheme, nil
}

func runController(args []string) error {
	flags := flag.NewFlagSet("controller", flag.ContinueOnError)
	metricsAddress := flags.String("metrics-bind-address", ":8080", "metrics listener")
	probeAddress := flags.String("health-probe-bind-address", ":8081", "health listener")
	webhookPort := flags.Int("webhook-port", 9443, "admission webhook port")
	certDir := flags.String("webhook-cert-dir", "/tmp/k8s-webhook-server/serving-certs", "admission certificate directory")
	leaderElection := flags.Bool("leader-elect", true, "enable leader election")
	leaderNamespace := flags.String("leader-election-namespace", os.Getenv("POD_NAMESPACE"), "leader election namespace")
	providerNamespace := flags.String("provider-namespace", os.Getenv("POD_NAMESPACE"), "namespace for splitter resources")
	providerImage := flags.String("provider-image", os.Getenv("TELEPRESENCE_KAFKA_IMAGE"), "image used by splitter StatefulSets")
	serviceAccount := flags.String("service-account", runtimeconfig.ProviderName, "service account used by splitter StatefulSets")
	zapOptions := zap.Options{Development: false}
	zapOptions.BindFlags(flags)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *providerNamespace == "" {
		return errors.New("provider namespace is required")
	}
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOptions)))
	scheme, err := newScheme()
	if err != nil {
		return err
	}
	manager, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Cache: ctrlcache.Options{ByObject: map[client.Object]ctrlcache.ByObject{
			&corev1.ConfigMap{}:     {Namespaces: map[string]ctrlcache.Config{*providerNamespace: {}}},
			&corev1.Service{}:       {Namespaces: map[string]ctrlcache.Config{*providerNamespace: {}}},
			&coordinationv1.Lease{}: {Namespaces: map[string]ctrlcache.Config{*providerNamespace: {}}},
		}},
		Client: client.Options{Cache: &client.CacheOptions{DisableFor: []client.Object{
			&corev1.Secret{},
		}}},
		Metrics:                       metricsserver.Options{BindAddress: *metricsAddress},
		HealthProbeBindAddress:        *probeAddress,
		LeaderElection:                *leaderElection,
		LeaderElectionID:              runtimeconfig.ProviderName,
		LeaderElectionNamespace:       *leaderNamespace,
		LeaderElectionReleaseOnCancel: true,
		WebhookServer:                 webhook.NewServer(webhook.Options{Port: *webhookPort, CertDir: *certDir}),
	})
	if err != nil {
		return fmt.Errorf("create Kafka controller manager: %w", err)
	}
	if err := (&controller.SplitReconciler{
		Client: manager.GetClient(), ProviderNamespace: *providerNamespace,
		ProviderImage: *providerImage, ServiceAccount: *serviceAccount,
	}).SetupWithManager(manager); err != nil {
		return fmt.Errorf("register KafkaSplit controller: %w", err)
	}
	if err := (&controller.RouteReconciler{
		Client: manager.GetClient(), ProviderNamespace: *providerNamespace,
	}).SetupWithManager(manager); err != nil {
		return fmt.Errorf("register KafkaRoute controller: %w", err)
	}
	controller.RegisterWebhooks(manager.GetWebhookServer(), manager.GetClient())
	if err := manager.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := manager.AddReadyzCheck("readyz", manager.GetWebhookServer().StartedChecker()); err != nil {
		return err
	}
	return manager.Start(ctrl.SetupSignalHandler())
}

func runSplitter(args []string) error {
	flags := flag.NewFlagSet("splitter", flag.ContinueOnError)
	configFile := flags.String("config", "/var/run/telepresence-kafka/config.json", "splitter configuration file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	bytes, err := os.ReadFile(filepath.Clean(*configFile))
	if err != nil {
		return fmt.Errorf("read Kafka splitter config: %w", err)
	}
	var config runtimeconfig.Config
	if err := json.Unmarshal(bytes, &config); err != nil {
		return fmt.Errorf("decode Kafka splitter config: %w", err)
	}
	scheme, err := newScheme()
	if err != nil {
		return err
	}
	restConfig := ctrl.GetConfigOrDie()
	kubeClient, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("create splitter Kubernetes client: %w", err)
	}
	ctx := ctrl.SetupSignalHandler()
	clientOptions, err := kafkaconfig.Options(ctx, kubeClient, config.Namespace, config.Connection)
	if err != nil {
		return err
	}
	ordinal, err := podOrdinal(os.Getenv("POD_NAME"))
	if err != nil {
		return err
	}
	var acknowledged atomic.Uint64
	control := splitterControl{
		client: kubeClient, podName: os.Getenv("POD_NAME"), acknowledged: &acknowledged,
	}
	if config.Control != nil {
		control.config = *config.Control
		config.Routes, err = control.readRoutingTable(ctx)
		if err != nil {
			return fmt.Errorf("read initial Kafka routing table: %w", err)
		}
	}
	splitter, err := kafkaintercept.NewSplitter(kafkaintercept.SplitterConfig{
		Brokers:         config.Connection.BootstrapServers,
		Group:           config.Group,
		InstanceID:      config.InstanceIDPrefix + strconv.Itoa(ordinal),
		TransactionalID: config.TransactionalIDPrefix + strconv.Itoa(ordinal),
		AppTopics:       config.AppTopics,
		OffsetReset:     config.OffsetReset,
		BatchSize:       config.BatchSize,
		ClientOptions:   clientOptions,
		InitialRoutes:   config.Routes,
		OnGeneration:    acknowledged.Store,
	})
	if err != nil {
		return err
	}
	if config.Control == nil {
		return splitter.Run(ctx)
	}
	controlCtx, cancelControl := context.WithCancel(ctx)
	defer cancelControl()
	go control.run(controlCtx, splitter)
	err = splitter.Run(ctx)
	cancelControl()
	publishCtx, cancelPublish := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancelPublish()
	control.publish(publishCtx, false)
	return err
}

func podOrdinal(name string) (int, error) {
	index := strings.LastIndexByte(name, '-')
	if index < 0 || index == len(name)-1 {
		return 0, fmt.Errorf("POD_NAME %q has no StatefulSet ordinal", name)
	}
	ordinal, err := strconv.Atoi(name[index+1:])
	if err != nil || ordinal < 0 {
		return 0, fmt.Errorf("POD_NAME %q has invalid StatefulSet ordinal", name)
	}
	return ordinal, nil
}
