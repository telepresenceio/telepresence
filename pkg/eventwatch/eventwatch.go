// Package eventwatch streams and classifies Kubernetes Warning events for a
// named workload (and the pods it owns), so callers can surface why a pod failed
// to become ready — ImagePullBackOff, FailedScheduling, and so on — instead of
// waiting out a timeout and reporting a generic error.
package eventwatch

import (
	"context"
	"strings"
	"time"

	events "k8s.io/api/events/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

// WatchWarnings streams Warning events (type != "Normal") for the object named
// name, or any pod it owns (matched by the "name-" prefix), in namespace. Only
// events created at or after the call are delivered. The returned channel stops
// receiving when ctx is done or the underlying watch ends.
//
// The kubernetes.Interface is passed explicitly so the watcher works both in the
// traffic-manager (in-cluster) and in the client (against the user's kubeconfig).
func WatchWarnings(ctx context.Context, ki kubernetes.Interface, namespace, name string) (<-chan *events.Event, error) {
	// A timestamp with second granularity is needed here, because that's what the event creation time uses.
	// Finer granularity will result in relevant events seemingly being created before this timestamp because
	// they have the fraction of seconds trimmed off (which is odd, given that the type used is a MicroTime).
	start := time.Unix(time.Now().Unix(), 0)

	ei := ki.EventsV1().Events(namespace)
	w, err := ei.Watch(ctx, meta.ListOptions{
		FieldSelector: fields.OneTermNotEqualSelector("type", "Normal").String(),
	})
	if err != nil {
		return nil, err
	}
	nd := name + "-"
	ec := make(chan *events.Event)
	go func() {
		defer w.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case eo, ok := <-w.ResultChan():
				if !ok {
					return
				}
				// Using negated Before when comparing the timestamps here is relevant. They will often be equal and still relevant
				if e, ok := eo.Object.(*events.Event); ok &&
					!e.CreationTimestamp.Time.Before(start) &&
					!strings.HasPrefix(e.Note, "(combined from similar events):") {
					n := e.Regarding.Name
					if strings.HasPrefix(n, nd) || n == name {
						clog.Infof(ctx, "%s %s %s", e.Type, e.Reason, e.Note)
						ec <- e
					}
				}
			}
		}
	}()
	return ec, nil
}

// transientNotes are substrings in an event's note that indicate a retryable
// condition — the action will be retried and may still succeed — so the event is
// not treated as terminal.
var transientNotes = []string{ //nolint:gochecknoglobals // effectively a constant
	"waiting for ephemeral volume",
	"unbound immediate PersistentVolumeClaims",
	"skip schedule deleting pod",
	"nodes are available",
	// Scheduler race: a pod was queued for binding but deleted (replaced during rollout)
	// before the bind completed. The scheduler retries automatically.
	"running Bind plugin",
}

// IsTerminal reports whether a Warning event names a failure that will not
// resolve on its own, so a caller can stop waiting and report it immediately
// rather than waiting for a timeout.
func IsTerminal(e *events.Event) bool {
	switch e.Reason {
	case "BackOff":
		// A container was created but fails to start (image pull back-off, crash loop).
		return true
	case "Failed", "FailedCreate", "FailedScheduling":
		// Usually fatal (bad image, resource quota, unschedulable), but some of
		// these are retried and may still succeed.
		if e.Type == "Warning" {
			for _, t := range transientNotes {
				if strings.Contains(e.Note, t) {
					return false
				}
			}
		}
		return true
	default:
		// Other warnings are often retried and eventually succeed.
		return false
	}
}

// WriteList renders es as a kubectl-style AGE/TYPE/REASON/OBJECT/MESSAGE table.
func WriteList(bf *strings.Builder, es []*events.Event) {
	now := time.Now()
	age := func(e *events.Event) string {
		return now.Sub(e.CreationTimestamp.Time).Truncate(time.Second).String()
	}
	object := func(e *events.Event) string {
		or := e.Regarding
		return strings.ToLower(or.Kind) + "/" + or.Name
	}
	ageLen, typeLen, reasonLen, objectLen := len("AGE"), len("TYPE"), len("REASON"), len("OBJECT")
	for _, e := range es {
		if l := len(age(e)); l > ageLen {
			ageLen = l
		}
		if l := len(e.Type); l > typeLen {
			typeLen = l
		}
		if l := len(e.Reason); l > reasonLen {
			reasonLen = l
		}
		if l := len(object(e)); l > objectLen {
			objectLen = l
		}
	}
	ageLen += 3
	typeLen += 3
	reasonLen += 3
	objectLen += 3
	ioutil.Printf(bf, "%-*s%-*s%-*s%-*s%s\n", ageLen, "AGE", typeLen, "TYPE", reasonLen, "REASON", objectLen, "OBJECT", "MESSAGE")
	for _, e := range es {
		ioutil.Printf(bf, "%-*s%-*s%-*s%-*s%s\n", ageLen, age(e), typeLen, e.Type, reasonLen, e.Reason, objectLen, object(e), e.Note)
	}
}
