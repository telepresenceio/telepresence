package managerutil

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/datawire/dlib/dlog"
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

type imagePoller struct {
	sync.RWMutex
	onChange func(context.Context, string) error
	img      string
}

func logAgentImageInfo(ctx context.Context, img string) {
	dlog.Infof(ctx, "Using traffic-agent image %q", img)
}

func (p *imagePoller) poll(ctx context.Context) {
	r, err := AgentImageFromSystemA(ctx)
	if err != nil {
		dlog.Warnf(ctx, "unable to get Ambassador Cloud preferred agent image: %v", err)
		return
	}
	diff := false
	p.Lock()
	if p.img != r {
		diff = true
		p.img = r
	}
	p.Unlock()
	if diff {
		logAgentImageInfo(ctx, r)
		if err := p.onChange(ctx, r); err != nil {
			dlog.Error(ctx, err)
		}
	}
}

func (p *imagePoller) start(ctx context.Context) {
	go func() {
		var timer *time.Timer
		defer timer.Stop()
		duration := func() time.Duration {
			if p.img == "" {
				// More aggressive poll until we have an image.
				return 20 * time.Second
			}
			return 5 * time.Minute
		}
		timer = time.AfterFunc(duration(), func() {
			p.poll(ctx)
			timer.Reset(duration())
		})
		<-ctx.Done()
	}()
}

func (p *imagePoller) GetImage() (img string) {
	p.RLock()
	img = p.img
	p.RUnlock()
	return
}

type irKey struct{}

// WithAgentImageRetriever returns a context that is configured with an agent image retriever which will
// retrieve the agent image from the environment variable AGENT_IMAGE or from the Ambassador Cloud, if
// that environment variable is empty. An error is returned if the environment variable is empty and
// access to Ambassador Cloud has not been configured.
//
// The Ambassador Cloud retriever might return an empty string when used, due to inability to contact
// Ambassador Cloud.
func WithAgentImageRetriever(ctx context.Context, onChange func(context.Context, string) error) (context.Context, error) {
	env := GetEnv(ctx)
	var ir ImageRetriever
	var img string
	if env.AgentImage == "" {
		var err error
		img, err = AgentImageFromSystemA(ctx)
		if err != nil {
			if strings.Contains(err.Error(), "not configured") {
				// No use polling when access isn't configured. This is normally prohibited by a Helm chart
				// assertion that either systemA is configured or AGENT_IMAGE is set.
				return ctx, err
			}
			dlog.Warnf(ctx, "unable to get Ambassador Cloud preferred agent image: %v", err)
		}

		// Set up an imagePoller to track changes in the preferred agent image
		ip := &imagePoller{img: img, onChange: onChange}
		ip.start(ctx)
		ir = ip
	} else {
		img = env.QualifiedAgentImage()
		ir = ImageFromEnv(img)
	}
	ctx = WithResolvedAgentImageRetriever(ctx, ir)
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

// GetAgentImage returns the fully qualified name of the traffic-agent image, i.e. "docker.io/tel2:2.7.4",
// or an empty string if no agent image has been configured.
func GetAgentImage(ctx context.Context) string {
	if ir, ok := ctx.Value(irKey{}).(ImageRetriever); ok {
		return ir.GetImage()
	}
	// The code isn't doing what it's supposed to do during startup.
	panic("no ImageRetriever has been configured")
}
