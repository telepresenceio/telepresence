package itest

import (
	"context"
	"strings"

	"github.com/telepresenceio/telepresence/v2/pkg/dos"
)

type Image struct {
	Name     string `json:"name,omitempty"`
	Tag      string `json:"tag,omitempty"`
	Registry string `json:"registry,omitempty"`
}

func (img *Image) FQName() string {
	nb := strings.Builder{}
	if img.Registry != "" {
		nb.WriteString(img.Registry)
		nb.WriteByte('/')
	}
	nb.WriteString(img.Name)
	if img.Tag != "" {
		nb.WriteByte(':')
		nb.WriteString(img.Tag)
	}
	return nb.String()
}

func ImageFromEnv(ctx context.Context, env, defaultTag, defaultRegistry string) *Image {
	if imgQN, ok := dos.LookupEnv(ctx, env); ok {
		img := new(Image)
		i := strings.LastIndexByte(imgQN, '/')
		if i >= 0 {
			img.Registry = imgQN[:i]
			imgQN = imgQN[i+1:]
		} else {
			img.Registry = defaultRegistry
		}
		if i = strings.IndexByte(imgQN, ':'); i > 0 {
			img.Name = imgQN[:i]
			img.Tag = imgQN[i+1:]
		} else {
			img.Name = imgQN
			img.Tag = defaultTag
		}
		return img
	}
	return nil
}

type imageContextKey struct{}

func WithImage(ctx context.Context, image *Image) context.Context {
	return context.WithValue(ctx, imageContextKey{}, image)
}

func GetImage(ctx context.Context) *Image {
	if image, ok := ctx.Value(imageContextKey{}).(*Image); ok {
		return image
	}
	return nil
}

type clientImageContextKey struct{}

func WithClientImage(ctx context.Context, image *Image) context.Context {
	return context.WithValue(ctx, clientImageContextKey{}, image)
}

func GetClientImage(ctx context.Context) *Image {
	if image, ok := ctx.Value(clientImageContextKey{}).(*Image); ok {
		return image
	}
	return nil
}

type agentImageContextKey struct{}

func WithAgentImage(ctx context.Context, image *Image) context.Context {
	return context.WithValue(ctx, agentImageContextKey{}, image)
}

func GetAgentImage(ctx context.Context) *Image {
	if image, ok := ctx.Value(agentImageContextKey{}).(*Image); ok {
		return image
	}
	return nil
}
