package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/datawire/dlib/dtime"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/rpc/v2/systema"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
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
func (cmc *cloudMessageCache) getCloudMessages() error {
	msgResponse := systema.CommandMessageResponse{
		Intercept: "Use `login` to connect to Ambassador Cloud and perform selective intercepts.",
	}
	cmc.Intercept = msgResponse.GetIntercept()
	return nil
}

// raiseCloudMessage is what is called from `PostRunE` in a command and is responsible
// for raising the message for the command used.
func raiseCloudMessage(cmd *cobra.Command, _ []string) error {
	// The command is the first word of cmd.Use
	cmdUsed := strings.Split(cmd.Use, " ")[0]

	// Load the config
	cmc, err := newCloudMessageCache(cmd.Context())
	if err != nil {
		return err
	}

	// Check if it is time to get new messages from Ambassador Cloud
	if dtime.Now().After(cmc.NextCheck) {
		err := cmc.getCloudMessages()
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
	_ = cache.SaveToUserCache(cmd.Context(), cmc, messagesCacheFilename)

	return nil
}
