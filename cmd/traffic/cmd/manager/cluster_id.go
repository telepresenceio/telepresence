package manager

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/datawire/ambassador/pkg/kates"
)

// BoolEmptyNonempty is a just a 'bool', but when used with go-envconfig, it parses by checking if
// the string is empty or nonempty, rather than using strconv.ParseBool as a regular 'bool' would
// do.
type BoolEmptyNonempty bool

func (b *BoolEmptyNonempty) EnvDecode(str string) error {
	*b = str != ""
	return nil
}

type ClusterEnv struct {
	AmbassadorClusterID       string            `env:"AMBASSADOR_CLUSTER_ID,default=$AMBASSADOR_SCOUT_ID"`
	AmbassadorSingleNamespace BoolEmptyNonempty `env:"AMBASSADOR_SINGLE_NAMESPACE,default="`
	AmbassadorNamespace       string            `env:"AMBASSADOR_NAMESPACE,default=default"`
	AmbassadorID              string            `env:"AMBASSADOR_ID,default=default"`
}

func GetClusterID(ctx context.Context, env ClusterEnv) string {
	clusterID := env.AmbassadorClusterID
	if clusterID != "" {
		return clusterID
	}

	rootID := func() (rootID string) {
		defer func() {
			// If kates panics, then we didn't make it to the end, so the default rootID
			// is still fine; and so we don't need to take any corrective action.
			//
			// PS: I can't express how frustrated I am that kates can panic.
			_ = recover()
		}()
		rootID = "00000000-0000-0000-0000-000000000000"

		client, err := kates.NewClient(kates.ClientOptions{})
		if err != nil {
			return
		}
		nsName := "default"
		if env.AmbassadorSingleNamespace {
			nsName = env.AmbassadorNamespace
		}
		ns := &kates.Namespace{
			TypeMeta:   kates.TypeMeta{Kind: "Namespace"},
			ObjectMeta: kates.ObjectMeta{Name: nsName},
		}

		if err := client.Get(ctx, ns, ns); err != nil {
			return
		}

		rootID = string(ns.GetUID())
		return
	}()

	clusterUrl := fmt.Sprintf("d6e_id://%s/%s", rootID, env.AmbassadorID)
	uid := uuid.NewSHA1(uuid.NameSpaceURL, []byte(clusterUrl))

	return strings.ToLower(uid.String())
}
