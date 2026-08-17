package rabbitmq

import (
	"context"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/queueagent/engine"
)

// Recover restores st's persisted phase -- never inferring from broker
// resources: st.Retained always refills retainedShadows, st.Started
// re-acquires the lock and reinstalls st.Routes, and the pump resumes only
// when neither st.Aborting nor st.Stopped is set. Aborting recovery leaves
// aborted false; the retried Abort sets it.
func (e *Engine) Recover(ctx context.Context, st engine.RecoveredState) error {
	if err := e.healthErr(); err != nil {
		return err
	}

	if _, err := e.Prepare(ctx); err != nil {
		return err
	}

	e.routesMu.Lock()
	for _, r := range st.Retained {
		e.retainedShadows[r.Name] = true
	}
	e.routesMu.Unlock()

	if !st.Started {
		return nil
	}
	e.Restore(st)

	if err := e.acquireLock(ctx); err != nil {
		return err
	}
	if err := e.ReconcileRoutes(ctx, st.Routes); err != nil {
		return err
	}
	if st.Aborting || st.Stopped {
		return nil
	}
	return e.resumePump(ctx)
}
