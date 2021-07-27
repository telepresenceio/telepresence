package cli

import (
	"testing"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/systema"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

func Test_cloudGetMessageFromCache(t *testing.T) {
	ctx := dlog.NewTestContext(t, false)

	// Create a fake user cache directory
	ctx = filelocation.WithUserHomeDir(ctx, t.TempDir())

	// Pre-load cmc with a message for intercept
	cmc, err := newCloudMessageCache(ctx)
	ceptMessage := "Test Intercept Message"
	cmc.Intercept = ceptMessage
	if err != nil {
		t.Error(err)
	}

	tests := []struct {
		name string
		cmd  string
		res  string
	}{
		{
			"command without a message",
			"leave",
			"",
		},
		{
			"command with a message",
			"intercept",
			ceptMessage,
		},
		{
			"second time running a command that has a message",
			"intercept",
			"",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// Test a command we don't have a message for
			res := cmc.getMessageFromCache(ctx, tc.cmd)
			if tc.res != res {
				t.Error("error: expected", tc.res, "received", res)
			}
			if val, ok := cmc.MessagesDelivered[tc.cmd]; !ok {
				t.Error("error: expected", tc.cmd, "to be present in MessagesDelivered")
			} else if !val {
				t.Error(" error: expected", tc.cmd, "to be true in MessagesDelivered")
			}
		})
	}
}

func Test_cloudUpdateMessages(t *testing.T) {
	ctx := dlog.NewTestContext(t, false)

	// Create a fake user cache directory
	ctx = filelocation.WithUserHomeDir(ctx, t.TempDir())

	// Pre-load cmc with a message for intercept
	cmc, err := newCloudMessageCache(ctx)
	ceptMessage := "Test Old Intercept Message"
	cmc.Intercept = ceptMessage
	if err != nil {
		t.Error(err)
	}

	// Ensure we get the current message in the cache
	res := cmc.getMessageFromCache(ctx, "intercept")
	if res != ceptMessage {
		t.Error("error: expected", ceptMessage, "received", res)
	}

	// Ensure we don't get a message since we just got one
	res = cmc.getMessageFromCache(ctx, "intercept")
	if res != "" {
		t.Error("error: expected", "", "received", res)
	}

	// Mock what we get from `GetUnauthenticatedCommandMessages`
	newCeptMsg := "New intercept message"
	updatedMessageResponse := &systema.CommandMessageResponse{
		Intercept: newCeptMsg,
	}
	cmc.updateCacheMessages(ctx, updatedMessageResponse)

	// Updating the messages resets `MessagesDelivered` so ensure
	// it is now false for "intercept"
	if val, ok := cmc.MessagesDelivered["intercept"]; !ok {
		t.Error("error: expected", "intercept", "to be present in MessagesDelivered")
	} else if val {
		t.Error(" error: expected", "intercept", "to be false in MessagesDelivered")
	}

	// Ensure we get the new message
	res = cmc.getMessageFromCache(ctx, "intercept")
	if res != newCeptMsg {
		t.Error("error: expected", newCeptMsg, "received", res)
	}

	// Ensure the NextCheck time is sufficiently in the future
	futureTime := time.Now().Add(time.Hour * 24 * 6)
	if !cmc.NextCheck.After(futureTime) {
		t.Error("error: expected nextCheck time to be greater than 6 days")
	}
}
