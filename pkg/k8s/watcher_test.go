package k8s_test

import (
	"testing"
	"time"

	"github.com/datawire/teleproxy/pkg/k8s"
	"github.com/stretchr/testify/require"
)

const (
	delay = 10 * time.Second
)

func fetch(w *k8s.Watcher, resource, qname string) (result k8s.Resource) {
	go func() {
		time.Sleep(delay)
		w.Stop()
	}()

	err := w.Watch(resource, func(w *k8s.Watcher) {
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

func info() *k8s.KubeInfo {
	return k8s.NewKubeInfo(CLUSTER_FILE, "", "")
}

func TestUpdateStatus(t *testing.T) {
	w := k8s.MustNewWatcher(info())

	svc := fetch(w, "services", "kubernetes.default")
	svc.Status()["loadBalancer"].(map[string]interface{})["ingress"] = []map[string]interface{}{{"hostname": "foo", "ip": "1.2.3.4"}}
	result, err := w.UpdateStatus(svc)
	if err != nil {
		t.Error(err)
		return
	} else {
		t.Logf("updated %s status, result: %v\n", svc.QName(), result.ResourceVersion())
	}

	svc = fetch(k8s.MustNewWatcher(info()), "services", "kubernetes.default")
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
	w := k8s.MustNewWatcher(info())

	// XXX: we can only watch custom resources... k8s doesn't
	// support status for CRDs until 1.12
	xmas := fetch(w, "customs", "xmas.default")
	if xmas == nil {
		t.Error("couldn't find xmas")
	} else {
		spec := xmas.Spec()
		if spec["deck"] != "the halls" {
			t.Errorf("expected the halls, got %v", spec["deck"])
		}
	}
}

func TestSelectiveWatch(t *testing.T) {
	w := k8s.MustNewWatcher(info())

	services := []string{}
	err := w.SelectiveWatch("", "services", "metadata.name=kubernetes", "", func(w *k8s.Watcher) {
		for _, r := range w.List("services") {
			services = append(services, r.QName())
		}
	})
	if err != nil {
		panic(err)
	}
	time.AfterFunc(1*time.Second, func() {
		w.Stop()
	})
	w.Wait()
	require.Equal(t, services, []string{"kubernetes.default"})
}
