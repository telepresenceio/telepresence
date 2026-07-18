package manifest

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ingest"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/intercept"
	tpgrpc "github.com/telepresenceio/telepresence/v2/pkg/grpc"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

type attachmentResult struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Action string `json:"action"`
	Detail string `json:"detail,omitempty"`
}

// lookupAttachment resolves the current daemon-side state of an attachment by name: the
// intercept namespace (which covers intercept, wiretap, and replace) is tried first, then the
// ingest namespace. At most one of the two return values is non-nil.
func lookupAttachment(ctx context.Context, a *Attachment) (*manager.InterceptInfo, *connector.IngestInfo, error) {
	ud := daemon.MustGetUserClient(ctx)
	ic, err := ud.GetIntercept(ctx, &manager.GetInterceptRequest{Name: interceptLookupName(a)})
	if err != nil {
		if status.Code(err) != codes.NotFound {
			return nil, nil, tpgrpc.FromGRPC(err)
		}
		ic = nil
	}
	if ic != nil {
		return ic, nil, nil
	}
	ig, err := ud.GetIngest(ctx, &connector.IngestIdentifier{
		WorkloadName:  a.Name,
		ContainerName: a.Container,
		Namespace:     a.Namespace,
	})
	if err != nil {
		if status.Code(err) != codes.NotFound {
			return nil, nil, tpgrpc.FromGRPC(err)
		}
		return nil, nil, nil
	}
	return nil, ig, nil
}

// removeExisting tears down whichever of the two (at most one is non-nil) currently exists.
func removeExisting(ctx context.Context, existingIntercept *manager.InterceptInfo, existingIngest *connector.IngestInfo) error {
	ud := daemon.MustGetUserClient(ctx)
	switch {
	case existingIntercept != nil:
		_, err := ud.RemoveIntercept(ctx, &manager.RemoveInterceptRequest2{Name: existingIntercept.Spec.Name})
		return tpgrpc.FromGRPC(err)
	case existingIngest != nil:
		_, err := ud.LeaveIngest(ctx, &connector.IngestIdentifier{
			WorkloadName:  existingIngest.Workload,
			ContainerName: existingIngest.Container,
			Namespace:     existingIngest.Namespace,
		})
		return tpgrpc.FromGRPC(err)
	default:
		return nil
	}
}

// createAttachmentNow builds the attachment's Command from the manifest and creates it detached,
// reusing the same state machinery that the imperative intercept/replace/wiretap/ingest commands
// use when invoked without a trailing command to run.
func createAttachmentNow(cmd *cobra.Command, a *Attachment) error {
	ctx := cmd.Context()
	if a.Type == TypeIngest {
		c, err := buildIngestCommand(cmd, a)
		if err != nil {
			return err
		}
		mountErr := c.MountFlags.ValidateConnected(ctx)
		return ingest.NewState(c, mountErr).Run(ctx)
	}
	c, err := buildInterceptLikeCommand(cmd, a)
	if err != nil {
		return err
	}
	mountErr := c.MountFlags.ValidateConnected(ctx)
	_, err = intercept.NewState(c, mountErr).Run(ctx)
	return err
}

// desiredInterceptSpec builds the InterceptSpec that creating c would send, without performing
// any RPC, for use as the "wanted" side of a drift comparison.
func desiredInterceptSpec(ctx context.Context, c *intercept.Command) (*manager.InterceptSpec, error) {
	req, err := intercept.NewState(c, nil).CreateRequest(ctx)
	if err != nil {
		return nil, err
	}
	return req.Spec, nil
}

// diffInterceptSpec compares only the fields the manifest attachment actually specifies against
// the daemon's view of an existing intercept/wiretap/replace. Fields with no server-side
// representation (such as env file paths) are never compared.
func diffInterceptSpec(a *Attachment, want *manager.InterceptSpec, existing *manager.InterceptInfo) []string {
	got := existing.Spec
	var diffs []string
	if want.Agent != got.Agent {
		diffs = append(diffs, fmt.Sprintf("workload: manifest=%q actual=%q", want.Agent, got.Agent))
	}
	if a.Namespace != "" && want.Namespace != got.Namespace {
		diffs = append(diffs, fmt.Sprintf("namespace: manifest=%q actual=%q", want.Namespace, got.Namespace))
	}
	if a.Service != "" && want.ServiceName != got.ServiceName {
		diffs = append(diffs, fmt.Sprintf("service: manifest=%q actual=%q", want.ServiceName, got.ServiceName))
	}
	if a.Container != "" && want.ContainerName != got.ContainerName {
		diffs = append(diffs, fmt.Sprintf("container: manifest=%q actual=%q", want.ContainerName, got.ContainerName))
	}
	if len(a.Ports) > 0 &&
		(want.PortIdentifier != got.PortIdentifier || want.TargetPort != got.TargetPort || !slices.Equal(want.PodPorts, got.PodPorts)) {
		diffs = append(diffs, fmt.Sprintf("ports: manifest=%v actual port=%s target-port=%d pod-ports=%v",
			a.Ports, got.PortIdentifier, got.TargetPort, got.PodPorts))
	}
	if a.Address != "" && want.TargetHost != got.TargetHost {
		diffs = append(diffs, fmt.Sprintf("address: manifest=%q actual=%q", want.TargetHost, got.TargetHost))
	}
	if want.Mechanism != got.Mechanism {
		diffs = append(diffs, fmt.Sprintf("mechanism: manifest=%q actual=%q", want.Mechanism, got.Mechanism))
	}
	if len(a.Metadata) > 0 && !maps.Equal(want.Metadata, got.Metadata) {
		diffs = append(diffs, "metadata differs")
	}
	if len(a.HTTPHeaders) > 0 && !maps.Equal(want.HeaderFilters, got.HeaderFilters) {
		diffs = append(diffs, "httpHeaders differ")
	}
	if len(a.HTTPPathEqualities)+len(a.HTTPPathPrefixes)+len(a.HTTPPathRegexps) > 0 && !slices.Equal(want.PathFilters, got.PathFilters) {
		diffs = append(diffs, "http path filters differ")
	}
	if want.Plaintext != got.Plaintext {
		diffs = append(diffs, fmt.Sprintf("plaintext: manifest=%v actual=%v", want.Plaintext, got.Plaintext))
	}
	if len(a.ToPod) > 0 && !slices.Equal(want.LocalPorts, got.LocalPorts) {
		diffs = append(diffs, "toPod differs")
	}
	if a.Mount != nil && a.Mount.Path != "" && a.Mount.Path != existing.ClientMountPoint {
		diffs = append(diffs, fmt.Sprintf("mount.path: manifest=%q actual=%q", a.Mount.Path, existing.ClientMountPoint))
	}
	if a.NodeAgent != nil && *a.NodeAgent != got.NodeAgent {
		diffs = append(diffs, fmt.Sprintf("nodeAgent: manifest=%v actual=%v", *a.NodeAgent, got.NodeAgent))
	}
	return diffs
}

// diffIngestSpec compares only the fields the manifest attachment actually specifies against the
// daemon's view of an existing ingest. IngestInfo doesn't echo nodeAgent or toPod/localPorts, so
// those are never drift-checked for an ingest.
func diffIngestSpec(a *Attachment, existing *connector.IngestInfo) []string {
	var diffs []string
	if a.Namespace != "" && a.Namespace != existing.Namespace {
		diffs = append(diffs, fmt.Sprintf("namespace: manifest=%q actual=%q", a.Namespace, existing.Namespace))
	}
	if a.Container != "" && a.Container != existing.Container {
		diffs = append(diffs, fmt.Sprintf("container: manifest=%q actual=%q", a.Container, existing.Container))
	}
	if a.Mount != nil && a.Mount.Path != "" && a.Mount.Path != existing.ClientMountPoint {
		diffs = append(diffs, fmt.Sprintf("mount.path: manifest=%q actual=%q", a.Mount.Path, existing.ClientMountPoint))
	}
	return diffs
}

// reconcileAttachment resolves a single manifest attachment's current daemon-side state and
// brings it in line, unless dryRun is set, in which case it only reports what would happen.
func reconcileAttachment(cmd *cobra.Command, a *Attachment, dryRun bool) (attachmentResult, error) {
	ctx := cmd.Context()
	res := attachmentResult{Name: a.Name, Type: string(a.Type)}

	existingIntercept, existingIngest, err := lookupAttachment(ctx, a)
	if err != nil {
		return res, err
	}

	var drift []string
	switch {
	case existingIntercept != nil:
		existingType := types.AttachmentTypeFromSpec(existingIntercept.Spec)
		if a.Type == TypeIngest || attachmentRPCType(a.Type) != existingType {
			drift = []string{fmt.Sprintf("type: manifest=%s actual=%s", a.Type, existingType)}
		} else {
			c, cerr := buildInterceptLikeCommand(cmd, a)
			if cerr != nil {
				return res, cerr
			}
			want, derr := desiredInterceptSpec(ctx, c)
			if derr != nil {
				return res, derr
			}
			drift = diffInterceptSpec(a, want, existingIntercept)
		}
	case existingIngest != nil:
		if a.Type != TypeIngest {
			drift = []string{fmt.Sprintf("type: manifest=%s actual=ingest", a.Type)}
		} else {
			drift = diffIngestSpec(a, existingIngest)
		}
	default:
		if dryRun {
			res.Action = "would-create"
			return res, nil
		}
		if err := createAttachmentNow(cmd, a); err != nil {
			return res, err
		}
		res.Action = "created"
		return res, nil
	}

	if len(drift) == 0 {
		res.Action = "unchanged"
		return res, nil
	}
	res.Detail = strings.Join(drift, "; ")
	if dryRun {
		res.Action = "would-re-create"
		return res, nil
	}
	if err := removeExisting(ctx, existingIntercept, existingIngest); err != nil {
		return res, err
	}
	if err := createAttachmentNow(cmd, a); err != nil {
		return res, err
	}
	res.Action = "re-created"
	return res, nil
}

// removeAttachment tears down a single manifest attachment for "telepresence delete"; a missing
// attachment is reported but is not an error.
func removeAttachment(cmd *cobra.Command, a *Attachment) (attachmentResult, error) {
	ctx := cmd.Context()
	res := attachmentResult{Name: a.Name, Type: string(a.Type)}
	existingIntercept, existingIngest, err := lookupAttachment(ctx, a)
	if err != nil {
		return res, err
	}
	if existingIntercept == nil && existingIngest == nil {
		res.Action = "absent"
		return res, nil
	}
	if err := removeExisting(ctx, existingIntercept, existingIngest); err != nil {
		return res, err
	}
	res.Action = "removed"
	return res, nil
}
