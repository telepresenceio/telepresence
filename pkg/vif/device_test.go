package vif

import (
	"errors"
	"testing"
)

func TestDeviceResultReturnsNilInterfaceOnError(t *testing.T) {
	dev, err := deviceResult(nil, errors.New("open tun failed"))
	if err == nil {
		t.Fatal("deviceResult returned nil error")
	}
	if dev != nil {
		t.Fatalf("deviceResult returned non-nil device on error: %#v", dev)
	}
}
