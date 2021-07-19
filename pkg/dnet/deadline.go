// atomicDeadline is substantially based on pipeDeadline from Go 1.17.1 net/pipe.go.
//
// Copyright 2021 Datawire. All rights reserved.
//
// Copyright 2010 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package dnet

import (
	"sync"
	"sync/atomic"
	"time"
)

type atomicDeadline struct {
	// cbMu and timerMu must not be held at the same time.

	// config

	cbMu sync.Locker // must be held to call cb
	cb   func()

	// state

	timerMu sync.Mutex // Guards timer
	timer   *time.Timer

	canceled int32 // atomic
}

func (d *atomicDeadline) set(t time.Time) {
	d.timerMu.Lock()

	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}

	if t.IsZero() {
		// Time is zero, then there is no deadline.
		d.timerMu.Unlock()
		d.cbMu.Lock()
		atomic.StoreInt32(&d.canceled, 0)
		d.cbMu.Unlock()
	} else if dur := time.Until(t); dur > 0 {
		// Time is in the future, set up a timer to cancel in the future.
		d.timer = time.AfterFunc(dur, func() {
			d.cbMu.Lock()
			atomic.StoreInt32(&d.canceled, 1)
			d.cb()
			d.cbMu.Unlock()
		})
		d.timerMu.Unlock()
	} else {
		// time is in the past, so cancel immediately.
		d.timerMu.Unlock()
		d.cbMu.Lock()
		atomic.StoreInt32(&d.canceled, 1)
		d.cb()
		d.cbMu.Unlock()
	}
}

func (d *atomicDeadline) isCanceled() bool {
	return atomic.LoadInt32(&d.canceled) != 0
}
