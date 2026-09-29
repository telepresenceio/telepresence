package main

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept"
	"github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/runtimeconfig"
)

const (
	// heartbeatInterval is how often a splitter member renews its Lease.
	// Must agree with memberLeaseDurationSeconds and with the staleness
	// threshold in controller.splitterMemberStatus.
	heartbeatInterval = 15 * time.Second
	// memberLeaseDurationSeconds is the Lease's advertised validity window.
	memberLeaseDurationSeconds = 45
	// cacheSyncTimeout bounds the wait for the routing ConfigMap watch to
	// deliver its initial state.
	cacheSyncTimeout = 30 * time.Second
)

// routingApplier is the part of *kafkaintercept.Splitter the control loop
// depends on, so tests can supply a fake.
type routingApplier interface {
	ApplyRoutingTable(kafkaintercept.RoutingTable) error
}

type splitterControl struct {
	client       client.Client
	clientset    kubernetes.Interface
	config       runtimeconfig.Control
	podName      string
	acknowledged *atomic.Uint64

	publishMu sync.Mutex
}

// run watches the routing ConfigMap and applies every generation it sees,
// and renews the member Lease on heartbeatInterval. It returns once ctx is
// done, or if the watch fails to establish its initial state in time.
func (c *splitterControl) run(ctx context.Context, splitter routingApplier) error {
	log := ctrl.LoggerFrom(ctx)
	factory := informers.NewSharedInformerFactoryWithOptions(
		c.clientset, 0,
		informers.WithNamespace(c.config.Namespace),
		informers.WithTweakListOptions(func(o *metav1.ListOptions) {
			o.FieldSelector = "metadata.name=" + c.config.ConfigMap
		}),
	)
	configMaps := factory.Core().V1().ConfigMaps().Informer()
	handleEvent := func(obj any) {
		configMap, ok := obj.(*corev1.ConfigMap)
		if !ok {
			return
		}
		table, err := runtimeconfig.RoutingFromConfigMap(configMap)
		if err != nil {
			log.Error(err, "read Kafka routing table")
			return
		}
		if err := splitter.ApplyRoutingTable(table); err != nil {
			log.Error(err, "apply Kafka routing table")
		}
	}
	if _, err := configMaps.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    handleEvent,
		UpdateFunc: func(_, obj any) { handleEvent(obj) },
	}); err != nil {
		return fmt.Errorf("watch Kafka routing ConfigMap: %w", err)
	}

	factory.Start(ctx.Done())
	syncCtx, cancel := context.WithTimeout(ctx, cacheSyncTimeout)
	defer cancel()
	if !cache.WaitForCacheSync(syncCtx.Done(), configMaps.HasSynced) {
		return fmt.Errorf("routing ConfigMap watch did not sync within %s", cacheSyncTimeout)
	}

	heartbeatTicker := time.NewTicker(heartbeatInterval)
	defer heartbeatTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-heartbeatTicker.C:
			c.publish(ctx, true)
		}
	}
}

func (c *splitterControl) readRoutingTable(ctx context.Context) (kafkaintercept.RoutingTable, error) {
	configMap := new(corev1.ConfigMap)
	key := client.ObjectKey{Namespace: c.config.Namespace, Name: c.config.ConfigMap}
	if err := c.client.Get(ctx, key, configMap); err != nil {
		return kafkaintercept.RoutingTable{}, err
	}
	return runtimeconfig.RoutingFromConfigMap(configMap)
}

// publish serializes Lease writes so the heartbeat loop and the splitter's
// generation acknowledgement never race on the same Update.
func (c *splitterControl) publish(ctx context.Context, healthy bool) {
	if c.config.MemberLeasePrefix == "" || c.podName == "" {
		return
	}
	ordinal, err := podOrdinal(c.podName)
	if err != nil {
		return
	}
	c.publishMu.Lock()
	defer c.publishMu.Unlock()
	name := c.config.MemberLeasePrefix + strconv.Itoa(ordinal)
	now := metav1.NewMicroTime(time.Now())
	duration := int32(memberLeaseDurationSeconds)
	key := client.ObjectKey{Namespace: c.config.Namespace, Name: name}
	lease := new(coordinationv1.Lease)
	err = c.client.Get(ctx, key, lease)
	if apierrors.IsNotFound(err) {
		lease = &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name}}
	} else if err != nil {
		return
	}
	lease.Spec.HolderIdentity = &c.podName
	lease.Spec.LeaseDurationSeconds = &duration
	lease.Spec.RenewTime = &now
	if lease.Labels == nil {
		lease.Labels = make(map[string]string)
	}
	lease.Labels[runtimeconfig.SplitLabel] = c.config.Split
	lease.Labels[runtimeconfig.SplitNamespaceLabel] = c.config.Namespace
	lease.Labels[runtimeconfig.SplitNameLabel] = c.config.SplitName
	if lease.Annotations == nil {
		lease.Annotations = make(map[string]string)
	}
	lease.Annotations[runtimeconfig.GenerationAnnotation] = strconv.FormatUint(c.acknowledged.Load(), 10)
	lease.Annotations[runtimeconfig.HealthyAnnotation] = strconv.FormatBool(healthy)
	if lease.ResourceVersion == "" {
		_ = c.client.Create(ctx, lease)
	} else {
		_ = c.client.Update(ctx, lease)
	}
}
