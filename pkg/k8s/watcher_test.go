package k8s

import (
	"testing"
	"time"
)

const (
	delay = 10 * time.Second
)

func (w *Watcher) fetch(resource, qname string) (result Resource) {
	go func() {
		time.Sleep(delay)
		w.Stop()
	}()

	err := w.Watch(resource, func(w *Watcher) {
		for _, r := range w.List(resource) {
			if r.QName() == qname {
				result = r
				w.Stop()
			}
		}
	})

	if err != nil {
		panic(err)
	}

	w.Wait()
	return result
}

func TestUpdateStatus(t *testing.T) {
	w := NewClient(nil).Watcher()

	svc := w.fetch("services", "kubernetes.default")
	svc.Status()["loadBalancer"].(map[string]interface{})["ingress"] = []map[string]interface{}{{"hostname": "foo", "ip": "1.2.3.4"}}
	result, err := w.UpdateStatus(svc)
	if err != nil {
		t.Error(err)
		return
	} else {
		t.Logf("updated %s status, result: %v\n", svc.QName(), result.ResourceVersion())
	}

	svc = NewClient(nil).Watcher().fetch("services", "kubernetes.default")
	ingresses := svc.Status()["loadBalancer"].(map[string]interface{})["ingress"].([]interface{})
	ingress := ingresses[0].(map[string]interface{})
	if ingress["hostname"] != "foo" {
		t.Error("expected foo")
	}

	if ingress["ip"] != "1.2.3.4" {
		t.Error("expected 1.2.3.4")
	}
}

func TestWatchCustom(t *testing.T) {
	w := NewClient(nil).Watcher()

	// XXX: we can only watch custom resources... k8s doesn't
	// support status for CRDs until 1.12
	xmas := w.fetch("customs", "xmas.default")
	if xmas == nil {
		t.Error("couldn't find xmas")
	} else {
		spec := xmas.Spec()
		if spec["deck"] != "the halls" {
			t.Errorf("expected the halls, got %v", spec["deck"])
		}
	}
}
