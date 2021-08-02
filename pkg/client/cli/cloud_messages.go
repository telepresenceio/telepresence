package cli

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/spf13/cobra"

	"github.com/datawire/dlib/dtime"

	"github.com/telepresenceio/telepresence/rpc/v2/systema"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
)

const messagesCacheFilename = "cloud-messages.json"

type cloudMessageCache struct {
	NextCheck         time.Time       `json:"next_check"`
	Intercept         string          `json:"intercept"`
	MessagesDelivered map[string]bool `json:"messagess_delivered"`
}

// newCloudMessageCache returns a new CloudMessageCache, initialized from the users' if it exists
func newCloudMessageCache(ctx context.Context) (*cloudMessageCache, error) {
	cmc := &cloudMessageCache{}

	if err := cache.LoadFromUserCache(ctx, cmc, messagesCacheFilename); err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		cmc.NextCheck = time.Time{}
		cmc.MessagesDelivered = make(map[string]bool)
		_ = cache.SaveToUserCache(ctx, cmc, messagesCacheFilename)
	}
	return cmc, nil
}

// getCloudMessages communicates with Ambassador Cloud and stores those messages
// in the cloudMessageCache.
// For now this function will just return a fake result from the cloud
// this will be replaced with the code to talk to Ambassador Cloud
// via GetUnauthenticatedCommandMessages in a later commit
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
	for key := range cmc.MessagesDelivered {
		cmc.MessagesDelivered[key] = false
	}
}

func (cmc *cloudMessageCache) getMessageFromCache(ctx context.Context, cmdUsed string) string {
	// Ensure that the message hasn't already been delivered to the user
	// if it has, then we don't want to print any output so as to not
	// annoy the user.
	var msg string
	if val, ok := cmc.MessagesDelivered[cmdUsed]; ok {
		if val {
			return msg
		}
	}

	// Check if we have a message for the given command
	switch cmdUsed {
	case "intercept":
		msg = cmc.Intercept
	default:
		// We don't currently have any messages for this command
		// so our msg will remain empty
	}
	cmc.MessagesDelivered[cmdUsed] = true
	return msg
}

// raiseCloudMessage is what is called from `PostRunE` in a command and is responsible
// for raising the message for the command used.
func raiseCloudMessage(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	// Currently we only have messages that should be served when a user
	// isn't logged in, so we check that here
	if cliutil.HasLoggedIn(cmd.Context()) {
		if _, err := cliutil.GetCloudUserInfo(ctx, false, true); err == nil {
			return nil
		}
	}

	// If the user has specified they are in an air-gapped cluster,
	// we shouldn't try to get messages
	if client.GetConfig(cmd.Context()).Cloud.SkipLogin {
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
		env, err := client.LoadEnv(ctx)
		if err != nil {
			return err
		}
		systemaURL := fmt.Sprintf("https://%s:%s", env.SystemAHost, env.SystemAPort)
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
	fmt.Fprintf(cmd.OutOrStdout(), "%s\n", msg)
	_ = cache.SaveToUserCache(ctx, cmc, messagesCacheFilename)

	return nil
}
