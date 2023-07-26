package cmd

import (
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/tracing"
)

func uploadTraces() *cobra.Command {
	return &cobra.Command{
		Use:  "upload-traces <zipFile> <jaeger target>",
		Args: cobra.ExactArgs(2),

		Short:         "Upload Traces",
		Long:          "Upload Traces to a Jaeger instance",
		RunE:          pushTraces,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
}

func traceClient(url string) otlptrace.Client {
	client := otlptracegrpc.NewClient(
		otlptracegrpc.WithEndpoint(url),
		otlptracegrpc.WithInsecure(),
	)
	return client
}

func pushTraces(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	ctx, cancel := context.WithTimeout(ctx, 1*time.Minute)
	defer cancel()
	zipFile, err := filepath.Abs(args[0])
	if err != nil {
		return err
	}
	jaegerTarget := args[1]

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

	client := traceClient(jaegerTarget)
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
	fmt.Fprintf(cmd.OutOrStderr(), "Trace file %s uploaded to %s\n", zipFile, jaegerTarget)
	return nil
}
