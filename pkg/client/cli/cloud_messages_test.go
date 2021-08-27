package cli

import (
	"context"
	"testing"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/systema"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

func newTestContext(t *testing.T) context.Context {
	ctx := dlog.NewTestContext(t, false)
	// Create a fake user cache directory
	ctx = filelocation.WithUserHomeDir(ctx, t.TempDir())

	env, err := client.LoadEnv(ctx)
	if err != nil {
		t.Error(err)
	}
	ctx = client.WithEnv(ctx, env)

	// Load config (will be default since home dir is fake)
	cfg, err := client.LoadConfig(ctx)
	if err != nil {
		t.Error(err)
	}
	return client.WithConfig(ctx, cfg)
}

func Test_cloudGetMessageFromCache(t *testing.T) {
	ctx := newTestContext(t)

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
			if _, ok := cmc.MessagesDelivered[tc.cmd]; !ok {
				t.Error("error: expected", tc.cmd, "to be present in MessagesDelivered")
			}
		})
	}
}

func Test_cloudUpdateMessages(t *testing.T) {
	ctx := newTestContext(t)

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
	// the "intercept" command is not in the map
	if _, ok := cmc.MessagesDelivered["intercept"]; ok {
		t.Error("error: expected", "intercept", "not to be present in MessagesDelivered")
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

	futureTime = time.Now().Add(time.Hour * 24 * 8)
	if cmc.NextCheck.After(futureTime) {
		t.Error("error: expected nextCheck time to be less than 8 days")
	}
}

func Test_cloudRefreshMessagesConfig(t *testing.T) {
	ctx := newTestContext(t)
	confDir := t.TempDir()

	// Update the config to a shorter time
	configYml := "cloud:\n  refreshMessages: 24h"
	ctx, err := client.SetConfig(ctx, confDir, configYml)
	if err != nil {
		t.Error(err)
	}

	cmc, err := newCloudMessageCache(ctx)
	if err != nil {
		t.Error(err)
	}
	// Mock what we get from `GetUnauthenticatedCommandMessages`
	ceptMessage := "Intercept message, testing config"
	updatedMessageResponse := &systema.CommandMessageResponse{
		Intercept: ceptMessage,
	}

	// Call updateCacheMessage to update cmc with new message + nextCheck
	cmc.updateCacheMessages(ctx, updatedMessageResponse)

	// Ensure the NextCheck time is sufficiently in the future
	futureTime := time.Now().Add(time.Hour * 22)
	if !cmc.NextCheck.After(futureTime) {
		t.Error("error: expected nextCheck time to be greater than 22 hours")
	}

	futureTime = time.Now().Add(time.Hour * 26)
	if cmc.NextCheck.After(futureTime) {
		t.Error("error: expected nextCheck time to be less than 26 hours")
	}
}
