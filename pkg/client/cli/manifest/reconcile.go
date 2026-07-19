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
	Name    string `json:"name"`
	Type    string `json:"type"`
	Action  string `json:"action"`
	Detail  string `json:"detail,omitempty"`
	Handler string `json:"handler,omitempty"`
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
// use when invoked without a trailing command to run. It returns the attachment's environment and
// the id under which a handler process must register with the daemon (matching what the
// imperative commands pass to AddHandler), for use by the caller when a.Command is declared.
func createAttachmentNow(cmd *cobra.Command, a *Attachment) (env map[string]string, handlerID string, err error) {
	ctx := cmd.Context()
	if a.Type == TypeIngest {
		c, err := buildIngestCommand(cmd, a)
		if err != nil {
			return nil, "", err
		}
		mountErr := c.MountFlags.ValidateConnected(ctx)
		if err := ingest.NewState(c, mountErr).Run(ctx); err != nil {
			return nil, "", err
		}
		ud := daemon.MustGetUserClient(ctx)
		ii, err := ud.GetIngest(ctx, &connector.IngestIdentifier{WorkloadName: a.Name, ContainerName: a.Container, Namespace: a.Namespace})
		if err != nil {
			return nil, "", tpgrpc.FromGRPC(err)
		}
		env = handlerEnv(ctx, ii.Environment, a.Name+"/"+ii.Container, ii.ClientMountPoint)
		return env, fmt.Sprintf("%s/%s/%s", a.Name, ii.Container, ii.Namespace), nil
	}
	c, err := buildInterceptLikeCommand(cmd, a)
	if err != nil {
		return nil, "", err
	}
	mountErr := c.MountFlags.ValidateConnected(ctx)
	info, err := intercept.NewState(c, mountErr).Run(ctx)
	if err != nil {
		return nil, "", err
	}
	return info.Environment, info.ID, nil
}

// startNewHandler clears any stale handler record for a (leftover from a previous manifest run)
// and, when a declares a command, starts it fresh against the freshly (re-)created attachment.
func startNewHandler(ctx context.Context, a *Attachment, env map[string]string, handlerID string) (string, error) {
	stopHandler(ctx, a)
	if a.Command == nil {
		return "", nil
	}
	if err := startHandler(ctx, a, env, handlerID); err != nil {
		return "", err
	}
	return "started", nil
}

// reconcileHandler applies the handler decision tree for an attachment whose spec is otherwise
// unchanged: a matching running handler is left alone, an exited or never-started one is started,
// a handler whose argv differs is restarted, and a handler command removed from the manifest is
// stopped. dryRun only reports what would happen.
func reconcileHandler(ctx context.Context, a *Attachment, env map[string]string, handlerID string, dryRun bool) (string, error) {
	running, argsEqual, rec := handlerStatus(ctx, a)
	if a.Command == nil {
		if rec == nil {
			return "", nil
		}
		if dryRun {
			return "would-stop", nil
		}
		stopHandler(ctx, a)
		return "stopped", nil
	}
	if running && argsEqual {
		return "", nil
	}
	if running {
		// argv differs
		if dryRun {
			return "would-restart", nil
		}
		stopHandler(ctx, a)
		if err := startHandler(ctx, a, env, handlerID); err != nil {
			return "", err
		}
		return "restarted", nil
	}
	// no recorded process, or it has exited
	if dryRun {
		return "would-start", nil
	}
	if err := startHandler(ctx, a, env, handlerID); err != nil {
		return "", err
	}
	return "started", nil
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
			if a.Command != nil {
				res.Handler = "would-start"
			}
			return res, nil
		}
		env, handlerID, err := createAttachmentNow(cmd, a)
		if err != nil {
			return res, err
		}
		res.Action = "created"
		if res.Handler, err = startNewHandler(ctx, a, env, handlerID); err != nil {
			return res, err
		}
		return res, nil
	}

	if len(drift) == 0 {
		res.Action = "unchanged"
		var env map[string]string
		var handlerID string
		if existingIntercept != nil {
			env = handlerEnv(ctx, existingIntercept.Environment, existingIntercept.Id, existingIntercept.ClientMountPoint)
			handlerID = existingIntercept.Id
		} else {
			env = handlerEnv(ctx, existingIngest.Environment, a.Name+"/"+existingIngest.Container, existingIngest.ClientMountPoint)
			handlerID = fmt.Sprintf("%s/%s/%s", a.Name, existingIngest.Container, existingIngest.Namespace)
		}
		handler, err := reconcileHandler(ctx, a, env, handlerID, dryRun)
		if err != nil {
			return res, err
		}
		res.Handler = handler
		return res, nil
	}
	res.Detail = strings.Join(drift, "; ")
	if dryRun {
		res.Action = "would-re-create"
		if a.Command != nil {
			res.Handler = "would-start"
		}
		return res, nil
	}
	if err := removeExisting(ctx, existingIntercept, existingIngest); err != nil {
		return res, err
	}
	env, handlerID, err := createAttachmentNow(cmd, a)
	if err != nil {
		return res, err
	}
	res.Action = "re-created"
	if res.Handler, err = startNewHandler(ctx, a, env, handlerID); err != nil {
		return res, err
	}
	return res, nil
}

// removeAttachment tears down a single manifest attachment for "telepresence delete"; a missing
// attachment is reported but is not an error. The daemon already terminates a registered handler
// when its attachment is removed; stopHandler here is the fallback and the client-side record
// cleanup, so an already-dead recorded pid is tolerated silently.
func removeAttachment(cmd *cobra.Command, a *Attachment) (attachmentResult, error) {
	ctx := cmd.Context()
	res := attachmentResult{Name: a.Name, Type: string(a.Type)}
	existingIntercept, existingIngest, err := lookupAttachment(ctx, a)
	if err != nil {
		return res, err
	}
	_, _, rec := handlerStatus(ctx, a)
	if existingIntercept == nil && existingIngest == nil {
		res.Action = "absent"
	} else {
		if err := removeExisting(ctx, existingIntercept, existingIngest); err != nil {
			return res, err
		}
		res.Action = "removed"
	}
	if stopHandler(ctx, a) || rec != nil {
		res.Handler = "stopped"
	}
	return res, nil
}
