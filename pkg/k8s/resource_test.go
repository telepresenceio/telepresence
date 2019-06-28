package k8s_test

import (
	"strconv"
	"testing"

	"github.com/datawire/teleproxy/pkg/k8s"
)

func TestQKind(t *testing.T) {
	testcases := []struct {
		Resource k8s.Resource
		QKind    string
	}{
		// Sane things we need to handle correcly
		{k8s.Resource{"apiVersion": "apps/v1", "kind": "Deployment"}, "Deployment.v1.apps"},
		{k8s.Resource{"apiVersion": "v1", "kind": "Service"}, "Service.v1."},
		// Insane things that shouldn't happen, but at least our function is well-defined
		{k8s.Resource{"kind": "KindOnly"}, "KindOnly.."},
		{k8s.Resource{"apiVersion": "group/version"}, ".version.group"},
		{k8s.Resource{}, ".."},
		{k8s.Resource{"kind": 7, "apiVersion": "v1"}, ".v1."},
		{k8s.Resource{"kind": "Pod", "apiVersion": 1}, "Pod.."},
	}
	for i, testcase := range testcases {
		t.Run(strconv.Itoa(i), func(testcase struct {
			Resource k8s.Resource
			QKind    string
		}) func(t *testing.T) {
			return func(t *testing.T) {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("fail: %#v.QKind() paniced: %v", testcase.Resource, r)
					}
				}()
				qkind := testcase.Resource.QKind()
				if qkind != testcase.QKind {
					t.Errorf("fail: %#v.QKind()=%#v, expected %#v", testcase.Resource, qkind, testcase.QKind)
				}
			}
		}(testcase))
	}
}
