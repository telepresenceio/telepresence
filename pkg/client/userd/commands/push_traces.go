package commands

import (
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/tracing"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
)

type pushTracesCommand struct {
	cmd *cobra.Command
}

func (*pushTracesCommand) group() string {
	return "Tracing"
}

func (c *pushTracesCommand) cobraCommand(ctx context.Context) *cobra.Command {
	if c.cmd != nil {
		return c.cmd
	}

	c.cmd = &cobra.Command{
		Use:  "upload-traces",
		Args: cobra.ExactArgs(2),

		Short: "Upload Traces",
		Long:  "Upload Traces to a Jaeger instance",
		RunE:  c.pushTraces,
	}

	return c.cmd
}

func (*pushTracesCommand) init(_ context.Context) {}

func (c *pushTracesCommand) traceClient(url string) (otlptrace.Client, error) {
	client := otlptracegrpc.NewClient(
		otlptracegrpc.WithEndpoint(url),
		otlptracegrpc.WithInsecure(),
	)
	return client, nil
}

func (c *pushTracesCommand) pushTraces(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	ctx, cancel := context.WithTimeout(ctx, 1*time.Minute)
	defer cancel()
	zipFile, jaegerTarget := args[0], args[1]
	if !filepath.IsAbs(zipFile) {
		zipFile = filepath.Join(GetCwd(ctx), zipFile)
	}
	f, err := os.Open(zipFile)
	if err != nil {
		return fmt.Errorf("failed to open %s: %w", zipFile, err)
	}
	defer f.Close()
	zipR, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("failed to unzip %s: %w", zipFile, err)
	}
	defer zipR.Close()
	client, err := c.traceClient(jaegerTarget)
	if err != nil {
		return err
	}
	pr := tracing.NewProtoReader(zipR, func() *tracepb.ResourceSpans { return &tracepb.ResourceSpans{} })
	spans, err := pr.ReadAll(ctx)
	if err != nil {
		return err
	}
	msg := &tracepb.TracesData{
		ResourceSpans: spans,
	}
	dlog.Debugf(ctx, "Starting upload of %d traces", len(msg.ResourceSpans))
	err = client.Start(ctx)
	if err != nil {
		return err
	}
	err = client.UploadTraces(ctx, msg.ResourceSpans)
	if err != nil {
		return err
	}
	err = client.Stop(ctx)
	if err != nil {
		return err
	}
	return nil
}
