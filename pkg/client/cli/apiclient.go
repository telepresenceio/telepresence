package cli

import (
	"context"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/api"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/internal/apiimpl"
)

// NewClient creates a new Telepresence API Client
var NewClient func(ctx context.Context) api.Client = apiimpl.NewClient //nolint:gochecknoglobals // extension point
