package main

import (
	"context"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli"
)

func main() {
	ctx := context.Background()
	cli.Main(ctx)
}
