package main

import (
	"fmt"

	"github.com/0xAX/notificator"
	"github.com/datawire/teleproxy/pkg/supervisor"
)

var notifyConfig *notificator.Notificator

// Notify displays a desktop banner notification to the user
func Notify(p *supervisor.Process, title string, message ...string) {
	if notifyConfig == nil {
		notifyConfig = notificator.New(notificator.Options{
			DefaultIcon: "",
			AppName:     "Playpen Daemon",
		})
	}
	var err error
	switch {
	case len(message) == 0:
		p.Logf("NOTIFY: %s", title)
		err = notifyConfig.Push(title, "", "", notificator.UR_NORMAL)
	case len(message) == 1:
		p.Logf("NOTIFY: %s: %s", title, message)
		err = notifyConfig.Push(title, message[0], "", notificator.UR_NORMAL)
	default:
		panic(fmt.Sprintf("NOTIFY message too long: %d", len(message)))
	}
	if err != nil {
		p.Logf("ERROR while notifying: %v", err)
	}
}

// MaybeNotify displays a notification only if a value changes
func MaybeNotify(p *supervisor.Process, name string, old, new bool) {
	if old != new {
		Notify(p, fmt.Sprintf("%s: %t -> %t", name, old, new))
	}
}
