package util

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/rpc/v2/systema"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
)

const messagesCacheFilename = "cloud-messages.json"

type cloudMessageCache struct {
	NextCheck         time.Time           `json:"next_check"`
	Intercept         string              `json:"intercept"`
	MessagesDelivered map[string]struct{} `json:"messagess_delivered"`
}

// newCloudMessageCache returns a new CloudMessageCache, initialized from the users' if it exists.
func newCloudMessageCache(ctx context.Context) (*cloudMessageCache, error) {
	cmc := &cloudMessageCache{}

	if err := cache.LoadFromUserCache(ctx, cmc, messagesCacheFilename); err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		cmc.NextCheck = time.Time{}
		cmc.MessagesDelivered = make(map[string]struct{})
		_ = cache.SaveToUserCache(ctx, cmc, messagesCacheFilename)
	}
	return cmc, nil
}

// getCloudMessages communicates with Ambassador Cloud and stores those messages
// in the cloudMessageCache.
func getCloudMessages(ctx context.Context, systemaURL string) (*systema.CommandMessageResponse, error) {
	u, err := url.Parse(systemaURL)
	if err != nil {
		return &systema.CommandMessageResponse{}, err
	}
	conn, err := grpc.DialContext(ctx,
		(&url.URL{Scheme: "dns", Path: "/" + u.Host}).String(),
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{ServerName: u.Hostname()})))
	if err != nil {
		return &systema.CommandMessageResponse{}, err
	}

	systemaClient := systema.NewSystemACliClient(conn)
	return systemaClient.GetUnauthenticatedCommandMessages(ctx, &empty.Empty{})
}

func (cmc *cloudMessageCache) updateCacheMessages(ctx context.Context, resp *systema.CommandMessageResponse) {
	// Update the messages
	cmc.Intercept = resp.GetIntercept()

	// Update the time to do the next check since we were successful
	refreshMsgs := client.GetConfig(ctx).Cloud.RefreshMessages
	cmc.NextCheck = dtime.Now().Add(refreshMsgs)

	// We reset the messages delivered for all commands since they
	// may have changed
	cmc.MessagesDelivered = make(map[string]struct{})
}

func (cmc *cloudMessageCache) getMessageFromCache(_ context.Context, cmdUsed string) string {
	// Ensure that the message hasn't already been delivered to the user
	// if it has, then we don't want to print any output.
	var msg string
	if _, ok := cmc.MessagesDelivered[cmdUsed]; ok {
		return msg
	}

	// Check if we have a message for the given command
	switch cmdUsed {
	case "intercept":
		msg = cmc.Intercept
	default:
		// We don't currently have any messages for this command
		// so our msg will remain empty
	}
	cmc.MessagesDelivered[cmdUsed] = struct{}{}
	return msg
}

// RaiseCloudMessage is what is called from `PostRunE` in a command and is responsible
// for raising the message for the command used.
func RaiseCloudMessage(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	// If the user has specified they are in an air-gapped cluster,
	// we shouldn't try to get messages
	cloudCfg := client.GetConfig(cmd.Context()).Cloud
	if cloudCfg.SkipLogin {
		return nil
	}

	// The command is the first word of cmd.Use
	cmdUsed := strings.Split(cmd.Use, " ")[0]

	// Load the config
	cmc, err := newCloudMessageCache(ctx)
	if err != nil {
		return err
	}

	// Check if it is time to get new messages from Ambassador Cloud
	if dtime.Now().After(cmc.NextCheck) {
		systemaURL := fmt.Sprintf("https://%s", net.JoinHostPort(cloudCfg.SystemaHost, cloudCfg.SystemaPort))
		resp, err := getCloudMessages(ctx, systemaURL)
		if err != nil {
			// We try again in an hour since we encountered an error
			cmc.NextCheck = dtime.Now().Add(1 * time.Hour)
		} else {
			cmc.updateCacheMessages(ctx, resp)
		}
	}

	// Get the message from the cache that should be delivered to the user
	msg := cmc.getMessageFromCache(ctx, cmdUsed)
	if msg != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "%s\n", msg)
	}
	_ = cache.SaveToUserCache(ctx, cmc, messagesCacheFilename)

	return nil
}
