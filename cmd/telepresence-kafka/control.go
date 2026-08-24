package main

import (
	"context"
	"strconv"
	"sync/atomic"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept"
	"github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/runtimeconfig"
)

type splitterControl struct {
	client       client.Client
	config       runtimeconfig.Control
	podName      string
	acknowledged *atomic.Uint64
}

func (c *splitterControl) run(ctx context.Context, splitter *kafkaintercept.Splitter) {
	log := ctrl.LoggerFrom(ctx)
	routingTicker := time.NewTicker(time.Second)
	heartbeatTicker := time.NewTicker(5 * time.Second)
	defer routingTicker.Stop()
	defer heartbeatTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-routingTicker.C:
			table, err := c.readRoutingTable(ctx)
			if err != nil {
				log.Error(err, "read Kafka routing table")
				continue
			}
			if err := splitter.ApplyRoutingTable(table); err != nil {
				log.Error(err, "apply Kafka routing table")
			}
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

func (c *splitterControl) publish(ctx context.Context, healthy bool) {
	if c.config.MemberLeasePrefix == "" || c.podName == "" {
		return
	}
	ordinal, err := podOrdinal(c.podName)
	if err != nil {
		return
	}
	name := c.config.MemberLeasePrefix + strconv.Itoa(ordinal)
	now := metav1.NewMicroTime(time.Now())
	duration := int32(15)
	key := client.ObjectKey{Namespace: c.config.Namespace, Name: name}
	lease := new(coordinationv1.Lease)
	err = c.client.Get(ctx, key, lease)
	if apierrors.IsNotFound(err) {
		lease = &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: key.Namespace, Name: key.Name, Labels: map[string]string{runtimeconfig.SplitLabel: c.config.Split},
			},
		}
	} else if err != nil {
		return
	}
	lease.Spec.HolderIdentity = &c.podName
	lease.Spec.LeaseDurationSeconds = &duration
	lease.Spec.RenewTime = &now
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
