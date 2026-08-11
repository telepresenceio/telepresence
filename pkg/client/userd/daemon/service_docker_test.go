//go:build docker

package daemon

import (
	"context"
	"strings"
	"testing"

	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

func TestDockerBuildRejectsFTPWithoutStoppingUserDaemon(t *testing.T) {
	cfg := client.GetDefaultConfig()
	cfg.Intercept().UseFtp = true
	ctx := client.WithConfig(context.Background(), cfg)

	svc := newService(ctx, func() {}, cfg, nil)
	ftpErr := svc.FuseFTPError()
	if ftpErr == nil {
		t.Fatal("expected docker build to report unavailable FTP mounts")
	}
	if !strings.Contains(ftpErr.Error(), "built without fuseftp support") {
		t.Fatalf("unexpected FTP availability error: %v", ftpErr)
	}

	wasContainerized := proc.RunningInContainer()
	proc.SetRunningInContainer(false)
	t.Cleanup(func() { proc.SetRunningInContainer(wasContainerized) })
	if _, err := svc.RemoteMountAvailability(ctx, &empty.Empty{}); err == nil {
		t.Fatal("expected FTP mount availability check to fail")
	} else if !strings.Contains(err.Error(), "built without fuseftp support") {
		t.Fatalf("unexpected mount availability error: %v", err)
	}
}
