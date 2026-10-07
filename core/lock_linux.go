//go:build linux

package main

import (
	"github.com/godbus/dbus/v5"
)

// Linux lock detection: listen for ActiveChanged on the freedesktop and
// GNOME ScreenSaver D-Bus interfaces. Any failure degrades silently.
func startLockMonitor(onLock func(bool)) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		logf("lock monitor: no session bus: %v", err)
		return
	}
	defer conn.Close()

	for _, iface := range []string{"org.freedesktop.ScreenSaver", "org.gnome.ScreenSaver"} {
		err := conn.AddMatchSignal(
			dbus.WithMatchInterface(iface),
			dbus.WithMatchMember("ActiveChanged"),
		)
		if err != nil {
			logf("lock monitor: AddMatchSignal %s: %v", iface, err)
			return
		}
	}

	ch := make(chan *dbus.Signal, 16)
	conn.Signal(ch)
	defer conn.RemoveSignal(ch)

	logf("lock monitor: D-Bus ScreenSaver signals active")
	for sig := range ch {
		if len(sig.Body) == 0 {
			continue
		}
		if active, ok := sig.Body[0].(bool); ok {
			onLock(active)
		}
	}
}
