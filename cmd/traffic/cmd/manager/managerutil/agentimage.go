package managerutil

import (
	"context"
	"runtime/debug"
	"strings"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

type ImageRetriever interface {
	GetImage() string
}

type ImageFromEnv string

func (p ImageFromEnv) GetImage() string {
	return string(p)
}

func LogAgentImageInfo(ctx context.Context, img string) {
	dlog.Infof(ctx, "Using traffic-agent image %q", img)
}

type irKey struct{}

// WithAgentImageRetriever returns a context that is configured with an agent image retriever which will
// retrieve the agent image from the environment variable AGENT_IMAGE. An error is returned if the environment
// variable is empty.
func WithAgentImageRetriever(ctx context.Context, onChange func(context.Context, string) error) (context.Context, error) {
	env := GetEnv(ctx)
	if env.AgentImageName == "" {
		env.AgentImageName = "tel2"
	}
	if env.AgentImageTag == "" {
		env.AgentImageTag = strings.TrimPrefix(version.Version, "v")
	}
	if env.AgentRegistry == "" {
		env.AgentRegistry = env.Registry
	}
	img := env.QualifiedAgentImage()
	ctx = WithResolvedAgentImageRetriever(ctx, ImageFromEnv(img))
	if img != "" {
		LogAgentImageInfo(ctx, img)
		if err := onChange(ctx, img); err != nil {
			dlog.Error(ctx, err)
		}
	}
	return ctx, nil
}

func WithResolvedAgentImageRetriever(ctx context.Context, ir ImageRetriever) context.Context {
	return context.WithValue(ctx, irKey{}, ir)
}

func GetAgentImageRetriever(ctx context.Context) ImageRetriever {
	if ir, ok := ctx.Value(irKey{}).(ImageRetriever); ok {
		return ir
	}
	return nil
}

// GetAgentImage returns the fully qualified name of the traffic-agent image, i.e. "docker.io/tel2:2.7.4",
// or an empty string if no agent image has been configured.
func GetAgentImage(ctx context.Context) string {
	if ir, ok := ctx.Value(irKey{}).(ImageRetriever); ok {
		return ir.GetImage()
	}
	// The code isn't doing what it's supposed to do during startup.
	dlog.Error(ctx, string(debug.Stack()))
	panic("no ImageRetriever has been configured")
}
