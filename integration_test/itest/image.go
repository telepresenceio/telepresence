package itest

import "context"

type Image struct {
	Name     string `json:"name"`
	Tag      string `json:"tag,omitempty"`
	Registry string `json:"registry,omitempty"`
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
