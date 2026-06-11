package trafficmgr

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func agentInstalled(wi workloadInfo) bool {
	return wi.agentState != manager.WorkloadInfo_NO_AGENT_UNSPECIFIED
}

func (s *session) setWorkload(namespace, name string, wi workloadInfo) {
	s.workloadsLock.Lock()
	nm := s.workloads[namespace]
	if nm == nil {
		nm = make(map[workloadInfoKey]workloadInfo)
		s.workloads[namespace] = nm
	}
	nm[workloadInfoKey{kind: manager.WorkloadInfo_DEPLOYMENT, name: name}] = wi
	for _, subscriber := range s.workloadSubscribers {
		select {
		case subscriber <- struct{}{}:
		default:
		}
	}
	s.workloadsLock.Unlock()
}

func TestWaitForWorkloadUpdate(t *testing.T) {
	newSession := func() *session {
		return &session{workloads: make(map[string]map[workloadInfoKey]workloadInfo)}
	}

	t.Run("predicate already satisfied", func(t *testing.T) {
		s := newSession()
		s.setWorkload("default", "echo", workloadInfo{agentState: manager.WorkloadInfo_INSTALLED})
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		assert.True(t, s.waitForWorkloadUpdate(ctx, "default", "echo", agentInstalled))
	})

	t.Run("predicate satisfied after update", func(t *testing.T) {
		s := newSession()
		s.setWorkload("default", "echo", workloadInfo{agentState: manager.WorkloadInfo_NO_AGENT_UNSPECIFIED})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		go func() {
			time.Sleep(10 * time.Millisecond)
			s.setWorkload("default", "echo", workloadInfo{agentState: manager.WorkloadInfo_INSTALLED})
		}()
		assert.True(t, s.waitForWorkloadUpdate(ctx, "default", "echo", agentInstalled))
	})

	t.Run("timeout", func(t *testing.T) {
		s := newSession()
		s.setWorkload("default", "echo", workloadInfo{agentState: manager.WorkloadInfo_NO_AGENT_UNSPECIFIED})
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		assert.False(t, s.waitForWorkloadUpdate(ctx, "default", "echo", agentInstalled))
	})
}
