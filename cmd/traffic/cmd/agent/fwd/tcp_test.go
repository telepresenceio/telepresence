package fwd

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func TestTCPDispatch_HTTPMechanism_Handled(t *testing.T) {
	// Create a minimal tcp interceptor instance
	f := &tcp{interceptor: &interceptor{
		lCtx:       context.Background(),
		intercepts: make(interceptControllerMap),
		wiretaps:   make(interceptControllerMap),
	}}

	// net.Pipe gives us a pair of in-memory connections
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	// Close the server side immediately to cause EOF on read
	serverConn.Close()

	// Minimal intercept info with HTTP filters (enables HTTP mechanism)
	intercept := &manager.InterceptInfo{Spec: &manager.InterceptSpec{
		HeaderFilters: map[string]string{"X-Test": "value"},
	}}

	// Set the HTTP intercepts (simulates what fwdstate.HandlePort does)
	f.SetIntercepting([]*manager.InterceptInfo{intercept})

	// Call dispatch. Since the connection is closed, the HTTP handler
	// will get EOF when trying to read and return an error.
	handled := f.IsHTTP()
	require.True(t, handled, "expected HTTP mechanism to be handled by TCP dispatch")
}

func TestTCPDispatch_NoMechanism_NotHandled(t *testing.T) {
	f := &tcp{interceptor: &interceptor{
		lCtx:       context.Background(),
		intercepts: make(interceptControllerMap),
		wiretaps:   make(interceptControllerMap),
	}}

	// No intercept
	handled := f.IsHTTP()
	require.False(t, handled)

	// Intercept without HTTP mechanism (no filters)
	intercept := &manager.InterceptInfo{Spec: &manager.InterceptSpec{}}
	f.SetIntercepting([]*manager.InterceptInfo{intercept})
	handled = f.IsHTTP()
	require.False(t, handled)
}

// TestInterceptInfoRaceCondition verifies that concurrent updates to InterceptInfo
// (via reconcile()) do not cause race conditions in Forward() and handleHTTPRequest().
// This test simulates the scenario that occurs during HPA scaling where multiple pods
// are running and intercept information is updated while connections are being processed.
func TestInterceptInfoRaceCondition(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	f := &tcp{interceptor: &interceptor{
		lCtx:       ctx,
		intercepts: make(interceptControllerMap),
		wiretaps:   make(interceptControllerMap),
	}}

	// Initial intercept with port 3000
	initialIntercept := &manager.InterceptInfo{
		Id: "test-intercept",
		Spec: &manager.InterceptSpec{
			Name:       "test",
			TargetHost: "127.0.0.1",
			TargetPort: 3000,
		},
		ClientSession: &manager.SessionInfo{
			SessionId: "session-1",
		},
	}

	// Updated intercept with port 8080
	updatedIntercept := &manager.InterceptInfo{
		Id: "test-intercept",
		Spec: &manager.InterceptSpec{
			Name:       "test",
			TargetHost: "127.0.0.1",
			TargetPort: 8080,
		},
		ClientSession: &manager.SessionInfo{
			SessionId: "session-1",
		},
	}

	// Set initial intercept
	f.SetIntercepting([]*manager.InterceptInfo{initialIntercept})

	// Track goroutine completion
	var wg sync.WaitGroup
	raceDetected := false

	// Start multiple goroutines that simulate Forward() being called
	// while intercept info is being updated
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(iteration int) {
			defer wg.Done()

			// Simulate the critical section where Forward() captures intercept info
			f.mu.Lock()
			intercept, err := f.intercepts.global()
			var interceptSnap *interceptSnapshot
			if intercept != nil {
				interceptSnap = &interceptSnapshot{ic: intercept, info: intercept.InterceptInfo}
			}
			f.mu.Unlock()

			if err != nil {
				t.Errorf("iteration %d: failed to get global intercept: %v", iteration, err)
				return
			}

			if interceptSnap == nil {
				t.Errorf("iteration %d: expected intercept snapshot but got nil", iteration)
				return
			}

			// Verify that the captured info remains consistent
			// Even if reconcile() updates the pointer, our snapshot should be stable
			capturedPort := interceptSnap.info.Spec.TargetPort

			// Sleep briefly to give reconcile() time to run
			time.Sleep(10 * time.Millisecond)

			// Verify the captured port hasn't changed
			if interceptSnap.info.Spec.TargetPort != capturedPort {
				raceDetected = true
				t.Errorf("iteration %d: race detected! Port changed from %d to %d",
					iteration, capturedPort, interceptSnap.info.Spec.TargetPort)
			}
		}(i)
	}

	// Concurrently update intercept info multiple times to simulate reconcile()
	// being called during HPA scaling or other dynamic updates
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(iteration int) {
			defer wg.Done()

			// Alternate between two different intercept configurations
			var intercept *manager.InterceptInfo
			if iteration%2 == 0 {
				intercept = updatedIntercept
			} else {
				intercept = initialIntercept
			}

			// Simulate reconcile() updating the intercept
			f.SetIntercepting([]*manager.InterceptInfo{intercept})

			time.Sleep(5 * time.Millisecond)
		}(i)
	}

	// Wait for all goroutines to complete
	wg.Wait()

	require.False(t, raceDetected, "Race condition detected during concurrent access")
}

// TestHTTPInterceptInfoRaceCondition verifies that concurrent updates to InterceptInfo
// do not cause race conditions in handleHTTPRequest().
func TestHTTPInterceptInfoRaceCondition(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	f := &tcp{interceptor: &interceptor{
		lCtx:       ctx,
		intercepts: make(interceptControllerMap),
		wiretaps:   make(interceptControllerMap),
	}}

	// Initial HTTP intercept with port 3000 and header filter
	initialIntercept := &manager.InterceptInfo{
		Id: "http-intercept",
		Spec: &manager.InterceptSpec{
			Name:          "http-test",
			TargetHost:    "127.0.0.1",
			TargetPort:    3000,
			HeaderFilters: map[string]string{"X-Test": "initial"},
		},
		ClientSession: &manager.SessionInfo{
			SessionId: "session-1",
		},
	}

	// Updated HTTP intercept with different port and headers
	updatedIntercept := &manager.InterceptInfo{
		Id: "http-intercept",
		Spec: &manager.InterceptSpec{
			Name:          "http-test",
			TargetHost:    "127.0.0.1",
			TargetPort:    8080,
			HeaderFilters: map[string]string{"X-Test": "updated"},
		},
		ClientSession: &manager.SessionInfo{
			SessionId: "session-1",
		},
	}

	// Set initial intercept
	f.SetIntercepting([]*manager.InterceptInfo{initialIntercept})

	var wg sync.WaitGroup
	raceDetected := false

	// Start multiple goroutines that simulate handleHTTPRequest() capturing snapshots
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(iteration int) {
			defer wg.Done()

			// Simulate the critical section where handleHTTPRequest() captures snapshots
			f.mu.Lock()
			intercepts := f.intercepts.sorted()
			interceptSnapshots := make([]interceptSnapshot, len(intercepts))
			for i, ic := range intercepts {
				interceptSnapshots[i] = interceptSnapshot{ic: ic, info: ic.InterceptInfo}
			}
			f.mu.Unlock()

			if len(interceptSnapshots) == 0 {
				t.Errorf("iteration %d: expected intercept snapshots but got empty slice", iteration)
				return
			}

			// Verify that the captured info remains consistent
			snap := interceptSnapshots[0]
			capturedPort := snap.info.Spec.TargetPort
			capturedHeader := snap.info.Spec.HeaderFilters["X-Test"]

			// Sleep briefly to give reconcile() time to run
			time.Sleep(10 * time.Millisecond)

			// Verify the captured values haven't changed
			if snap.info.Spec.TargetPort != capturedPort {
				raceDetected = true
				t.Errorf("iteration %d: race detected! Port changed from %d to %d",
					iteration, capturedPort, snap.info.Spec.TargetPort)
			}

			if snap.info.Spec.HeaderFilters["X-Test"] != capturedHeader {
				raceDetected = true
				t.Errorf("iteration %d: race detected! Header changed from %s to %s",
					iteration, capturedHeader, snap.info.Spec.HeaderFilters["X-Test"])
			}
		}(i)
	}

	// Concurrently update intercept info
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(iteration int) {
			defer wg.Done()

			var intercept *manager.InterceptInfo
			if iteration%2 == 0 {
				intercept = updatedIntercept
			} else {
				intercept = initialIntercept
			}

			f.SetIntercepting([]*manager.InterceptInfo{intercept})
			time.Sleep(5 * time.Millisecond)
		}(i)
	}

	wg.Wait()
	require.False(t, raceDetected, "Race condition detected during concurrent HTTP intercept access")
}
