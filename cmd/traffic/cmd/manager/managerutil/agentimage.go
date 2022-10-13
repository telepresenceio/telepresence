package managerutil

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/datawire/dlib/dlog"
)

type imageRetriever interface {
	getImage() string
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
		dlog.Errorf(ctx, "unable to get Ambassador Cloud preferred agent image: %v", err)
	}
	diff := false
	p.Lock()
	if p.img == "" {
		// First time. Default to environment if fetch from SystemA failed.
		if r == "" {
			r = GetEnv(ctx).QualifiedAgentImage()
		}
		logAgentImageInfo(ctx, r)
		p.img = r
	} else if r != "" && r != p.img {
		// Subsequent times only updates if r is valid and if it differs from p.img
		diff = true
		logAgentImageInfo(ctx, r)
		p.img = r
	}
	p.Unlock()
	if diff && p.onChange != nil {
		if err := p.onChange(ctx, r); err != nil {
			dlog.Error(ctx, err)
		}
	}
}

func (p *imagePoller) start(ctx context.Context) {
	p.poll(ctx)
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.poll(ctx)
			}
		}
	}()
}

func (p *imagePoller) getImage() (img string) {
	p.RLock()
	img = p.img
	p.RUnlock()
	return
}

type imageFromEnv string

func (p imageFromEnv) getImage() string {
	return string(p)
}

type irKey struct{}

func WithAgentImageRetriever(ctx context.Context, onChange func(context.Context, string) error) context.Context {
	env := GetEnv(ctx)
	var ir imageRetriever
	if env.AgentImage == "" {
		ip := new(imagePoller)
		ip.onChange = onChange
		ip.start(ctx)
		ir = ip
	} else {
		ir = imageFromEnv(env.QualifiedAgentImage())
		logAgentImageInfo(ctx, ir.getImage())
	}
	return context.WithValue(ctx, irKey{}, ir)
}

// GetAgentImage returns the fully qualified name of the traffic-agent image, i.e. "docker.io/tel2:2.7.4".
func GetAgentImage(ctx context.Context) string {
	if ir, ok := ctx.Value(irKey{}).(imageRetriever); ok {
		return ir.getImage()
	}
	panic("no ImageRetriever has been configured")
}

// GetExtendedAgentImage returns the fully qualified name of the extended traffic-agent image, e.g.
// "docker.io/datawire/ambassador-telepresence-agent:1.12.8", or error indicating that the image name
// doesn't match.
func GetExtendedAgentImage(ctx context.Context) (string, error) {
	img := GetAgentImage(ctx)
	if si := strings.LastIndexByte(img, '/'); si > 0 && strings.HasPrefix(img[si:], "/ambassador-telepresence-agent:") {
		return img, nil
	}
	return "", fmt.Errorf("%q doesn't appear to be the name of an extended ambassador traffic-agent", img)
}
