//go:build linux

package dbus

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/telepresenceio/clog"
)

const (
	nmService       = "org.freedesktop.NetworkManager"
	nmPath          = "/org/freedesktop/NetworkManager"
	nmDevice        = nmService + ".Device"
	nmUnknownDevice = nmService + ".UnknownDevice"
)

// UnmanageNetworkManagerDevice tells NetworkManager not to manage the named network interface, so
// that it doesn't activate, configure, or probe connectivity through it. The setting lasts for the
// lifetime of the interface. Nothing is done when there is no system bus or NetworkManager isn't
// running. The call waits at most timeout for NetworkManager to discover a new interface.
func UnmanageNetworkManagerDevice(ctx context.Context, name string, timeout time.Duration) error {
	conn, err := dbus.ConnectSystemBus(dbus.WithContext(ctx))
	if err != nil {
		clog.Debugf(ctx, "not configuring NetworkManager: failed to connect to system bus: %v", err)
		return nil
	}
	defer conn.Close()

	var running bool
	if err = conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, nmService).Store(&running); err != nil {
		return err
	}
	if !running {
		clog.Debug(ctx, "not configuring NetworkManager: it is not running")
		return nil
	}

	path, err := nmDevicePath(ctx, conn, name, timeout)
	if err != nil {
		return err
	}
	return conn.Object(nmService, path).CallWithContext(
		ctx, "org.freedesktop.DBus.Properties.Set", 0, nmDevice, "Managed", dbus.MakeVariant(false)).Err
}

// nmDevicePath returns the path of NetworkManager's device object for the named interface,
// waiting for NetworkManager to announce the device if it hasn't discovered it yet.
func nmDevicePath(ctx context.Context, conn *dbus.Conn, name string, timeout time.Duration) (dbus.ObjectPath, error) {
	err := conn.AddMatchSignalContext(ctx,
		dbus.WithMatchObjectPath(nmPath),
		dbus.WithMatchInterface(nmService),
		dbus.WithMatchMember("DeviceAdded"))
	if err != nil {
		return "", err
	}
	added := make(chan *dbus.Signal, 10)
	conn.Signal(added)
	defer conn.RemoveSignal(added)

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	nm := conn.Object(nmService, nmPath)
	for {
		var path dbus.ObjectPath
		err = nm.CallWithContext(ctx, nmService+".GetDeviceByIpIface", 0, name).Store(&path)
		if err == nil {
			return path, nil
		}
		var dErr dbus.Error
		if !errors.As(err, &dErr) || dErr.Name != nmUnknownDevice {
			return "", err
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("NetworkManager did not discover device %s: %w", name, ctx.Err())
		case <-added:
		}
	}
}
