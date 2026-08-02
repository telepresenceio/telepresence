package manifest

import (
	"context"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/env"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/flags"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ingest"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/intercept"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/mount"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// interceptLookupName returns the InterceptSpec.Name that the given attachment would have (or
// already has) on the daemon side. Only "replace" folds the container into the name; intercept
// and wiretap keep container as a separate spec field.
func interceptLookupName(a *Attachment) string {
	if a.Type == TypeReplace && a.Container != "" {
		return a.Name + "/" + a.Container
	}
	return a.Name
}

func portStrings(ports []PortIdentifier) []string {
	out := make([]string, len(ports))
	for i, p := range ports {
		out[i] = p.String()
	}
	return out
}

func usesHTTPFilters(a *Attachment) bool {
	return len(a.HTTPHeaders) > 0 || len(a.HTTPPathEqualities) > 0 || len(a.HTTPPathPrefixes) > 0 || len(a.HTTPPathRegexps) > 0
}

// resolveMechanism mirrors intercept.Command.Validate's HTTP auto-detection: any HTTP filter
// forces the "http" mechanism, and HTTP together with a UDP port is a user error.
func resolveMechanism(a *Attachment, ports []string) (string, error) {
	mechanism := a.Mechanism
	if mechanism == "" {
		mechanism = "tcp"
	}
	if usesHTTPFilters(a) {
		for _, p := range ports {
			if pp, err := types.ParsePortAndProto(p); err == nil && pp.Proto == types.ProtoUDP {
				return "", errcat.User.Newf("HTTP filters cannot be used with UDP port %s", p)
			}
		}
		mechanism = "http"
	}
	return mechanism, nil
}

func buildEnvFlags(e *Env) (env.Flags, error) {
	var ef env.Flags
	if e == nil {
		return ef, nil
	}
	ef.File = e.File
	ef.JSON = e.JSON
	if e.Syntax != "" {
		if err := ef.Syntax.Set(e.Syntax); err != nil {
			return ef, errcat.User.New(err)
		}
	}
	return ef, nil
}

// buildMountFlags mirrors mount.Flags.AddFlags' defaults (mount enabled, no explicit path) plus
// the manifest's structured overrides. forceReadOnly mirrors the forceReadOnly parameter that
// ingest and wiretap pass to AddFlags.
func buildMountFlags(a *Attachment, forceReadOnly bool) mount.Flags {
	mf := mount.Flags{Enabled: true}
	if a.Mount != nil {
		mf.Enabled = a.Mount.IsEnabled()
		mf.Mount = a.Mount.Path
		mf.LocalMountPort = a.Mount.LocalMountPort
		mf.ReadOnly = a.Mount.ReadOnly
	}
	if forceReadOnly {
		mf.ReadOnly = true
	}
	return mf
}

// validateMountFlags replicates the one check of mount.Flags.Validate that still applies once
// the manifest has already supplied structured mount fields (the rest of Validate exists only to
// parse the tri-state --mount flag string, which the manifest doesn't have).
func validateMountFlags(ctx context.Context, mf *mount.Flags) error {
	if mf.LocalMountPort > 0 && client.GetConfig(ctx).Intercept().UseFtp {
		return errcat.User.New("only SFTP can be used with --local-mount-port. Client is configured to perform remote mounts using FTP")
	}
	return nil
}

// desiredNodeAgent resolves the effective node-agent setting for an attachment: an explicit
// manifest value always wins; otherwise it falls back to the same session-config-driven default
// that flags.NodeAgentDefault applies for a --node-agent flag that was never changed (cmd here
// has no such flag registered, so Changed() is always false and the fallback always applies).
func desiredNodeAgent(cmd *cobra.Command, a *Attachment) bool {
	if a.NodeAgent != nil {
		return *a.NodeAgent
	}
	return flags.NodeAgentDefault(cmd, false)
}

func metadataKeyValues(md map[string]string) []string {
	kvs := make([]string, 0, len(md))
	for k, v := range md {
		kvs = append(kvs, k+"="+v)
	}
	return kvs
}

// buildInterceptCommand maps an intercept or wiretap attachment onto the same intercept.Command
// struct that the "telepresence intercept"/"telepresence wiretap" commands build from flags.
func buildInterceptCommand(cmd *cobra.Command, a *Attachment) (*intercept.Command, error) {
	if err := intercept.ValidateKeyValues(a.HTTPHeaders); err != nil {
		return nil, err
	}
	ports := portStrings(a.Ports)
	mechanism, err := resolveMechanism(a, ports)
	if err != nil {
		return nil, err
	}
	agentName := a.Workload
	if agentName == "" {
		agentName = a.Name
	}
	if len(ports) == 0 {
		if dp := client.GetConfig(cmd.Context()).Intercept().DefaultPort; dp != 0 {
			ports = []string{strconv.Itoa(dp)}
		}
	}
	ef, err := buildEnvFlags(a.Env)
	if err != nil {
		return nil, err
	}
	wiretap := a.Type == TypeWiretap
	mf := buildMountFlags(a, wiretap)
	if err := validateMountFlags(cmd.Context(), &mf); err != nil {
		return nil, err
	}
	c := &intercept.Command{
		EnvFlags:              ef,
		MountFlags:            mf,
		Name:                  a.Name,
		AgentName:             agentName,
		Namespace:             a.Namespace,
		Ports:                 ports,
		ServiceName:           a.Service,
		ContainerName:         a.Container,
		Address:               a.Address,
		Wiretap:               wiretap,
		NodeAgent:             desiredNodeAgent(cmd, a),
		ToPod:                 append([]string(nil), a.ToPod...),
		Mechanism:             mechanism,
		Metadata:              metadataKeyValues(a.Metadata),
		HTTPHeaderFilters:     append([]string(nil), a.HTTPHeaders...),
		HTTPPathEqualFilters:  append([]string(nil), a.HTTPPathEqualities...),
		HTTPPathPrefixFilters: append([]string(nil), a.HTTPPathPrefixes...),
		HTTPPathRegexFilters:  append([]string(nil), a.HTTPPathRegexps...),
		Plaintext:             a.Plaintext,
		// printSummary emits the command's single output object; an inner
		// attachment operation must never emit its own.
		FormattedOutput: false,
	}
	return c, nil
}

// buildReplaceCommand maps a replace attachment onto intercept.Command, mirroring
// intercept.Command.ValidateReplace.
func buildReplaceCommand(cmd *cobra.Command, a *Attachment) (*intercept.Command, error) {
	name := a.Name
	agentName := name
	if i := strings.IndexByte(name, '/'); i >= 0 {
		agentName = name[:i]
	}
	if a.Container != "" {
		name = a.Name + "/" + a.Container
	}
	ports := portStrings(a.Ports)
	if len(ports) == 0 {
		ports = []string{"all"}
	}
	for i := range ports {
		if ports[i] == "all" {
			ports[i] = ":all"
		}
	}
	ef, err := buildEnvFlags(a.Env)
	if err != nil {
		return nil, err
	}
	mf := buildMountFlags(a, false)
	if err := validateMountFlags(cmd.Context(), &mf); err != nil {
		return nil, err
	}
	c := &intercept.Command{
		EnvFlags:      ef,
		MountFlags:    mf,
		Name:          name,
		AgentName:     agentName,
		Namespace:     a.Namespace,
		Ports:         ports,
		Address:       a.Address,
		ContainerName: a.Container,
		Replace:       true,
		NodeAgent:     desiredNodeAgent(cmd, a),
		ToPod:         append([]string(nil), a.ToPod...),
		Mechanism:     "tcp",
		NoDefaultPort: true,
		// printSummary emits the command's single output object; an inner
		// attachment operation must never emit its own.
		FormattedOutput: false,
	}
	return c, nil
}

// buildInterceptLikeCommand dispatches between the intercept/wiretap and replace mappings; it
// must not be called for an ingest attachment.
func buildInterceptLikeCommand(cmd *cobra.Command, a *Attachment) (*intercept.Command, error) {
	if a.Type == TypeReplace {
		return buildReplaceCommand(cmd, a)
	}
	return buildInterceptCommand(cmd, a)
}

// buildIngestCommand maps an ingest attachment onto ingest.Command.
func buildIngestCommand(cmd *cobra.Command, a *Attachment) (*ingest.Command, error) {
	ef, err := buildEnvFlags(a.Env)
	if err != nil {
		return nil, err
	}
	mf := buildMountFlags(a, true)
	if err := validateMountFlags(cmd.Context(), &mf); err != nil {
		return nil, err
	}
	c := &ingest.Command{
		EnvFlags:      ef,
		MountFlags:    mf,
		WorkloadName:  a.Name,
		ContainerName: a.Container,
		Namespace:     a.Namespace,
		ToPod:         append([]string(nil), a.ToPod...),
		NodeAgent:     desiredNodeAgent(cmd, a),
		// printSummary emits the command's single output object; an inner
		// attachment operation must never emit its own.
		FormattedOutput: false,
	}
	return c, nil
}

func attachmentRPCType(t AttachmentType) types.AttachmentType {
	switch t {
	case TypeWiretap:
		return types.AttachmentTypeWiretap
	case TypeReplace:
		return types.AttachmentTypeReplace
	case TypeIngest:
		return types.AttachmentTypeIngest
	default:
		return types.AttachmentTypeIntercept
	}
}
