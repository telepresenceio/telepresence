package k8s

import (
	"testing"
)

func TestList(t *testing.T) {
	c := NewClient(nil)
	svcs, err := c.List("svc")
	if err != nil {
		t.Error(err)
	}
	found := false
	for _, svc := range svcs {
		if svc.Name() == "kubernetes" {
			found = true
		}
	}
	if !found {
		t.Errorf("did not find kubernetes service")
	}

	customs, err := c.List("customs")
	found = false
	for _, cust := range customs {
		if cust.Name() == "xmas" {
			found = true
		}
	}

	if !found {
		t.Errorf("did not find xmas")
	}
}
