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
func (cmc *cloudMessageCache) getCloudMessages(ctx context.Context, systemaURL string) error {
	u, err := url.Parse(systemaURL)
	if err != nil {
		return err
	}
	conn, err := grpc.DialContext(ctx,
		(&url.URL{Scheme: "dns", Path: "/" + u.Host}).String(),
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{ServerName: u.Hostname()})))
	if err != nil {
		return err
	}

	systemaClient := systema.NewSystemACliClient(conn)
	resp, err := systemaClient.GetUnauthenticatedCommandMessages(ctx, &empty.Empty{})
	if err != nil {
		/*
			we can uncomment this to 'fake' it while we wait for Ambassador Cloud
			to implement this RPC

			msgResponse := systema.CommandMessageResponse{
				Intercept: "Use `login` to connect to Ambassador Cloud and perform selective intercepts.",
			}
			cmc.Intercept = msgResponse.GetIntercept()
		*/
		return err
	}
	cmc.Intercept = resp.GetIntercept()
	return nil
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
		err = cmc.getCloudMessages(ctx, systemaURL)
		if err != nil {
			// We try again in an hour since we encountered an error
			cmc.NextCheck = dtime.Now().Add(1 * time.Hour)
		} else {
			// This was successful so we'll get updates in a week
			cmc.NextCheck = dtime.Now().Add(24 * 7 * time.Hour)
			// We reset the messages delivered for all commands since they
			// may have changed
			for key := range cmc.MessagesDelivered {
				cmc.MessagesDelivered[key] = false
			}
		}
	}

	// Ensure that the message hasn't already been delivered to the user
	// if it has, then we don't want to print any output so as to not
	// annoy the user.
	if val, ok := cmc.MessagesDelivered[cmdUsed]; ok {
		if val {
			return nil
		}
	}

	// Check if we have a message for the given command
	var msg string
	switch cmdUsed {
	case "intercept":
		msg = cmc.Intercept
	default:
		// We don't currently have any messages for this command
		// so our msg will be empty
	}
	cmc.MessagesDelivered[cmdUsed] = true
	fmt.Fprintf(cmd.OutOrStdout(), "%s", msg)
	_ = cache.SaveToUserCache(ctx, cmc, messagesCacheFilename)

	return nil
}
