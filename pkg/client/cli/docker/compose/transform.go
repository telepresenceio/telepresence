package compose

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"

	compose "github.com/compose-spec/compose-go/v2/types"
	"github.com/puzpuzpuz/xsync/v4"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/flags"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/progress"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/json"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/shellquote"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

type transformer struct {
	config      *config
	project     *compose.Project
	extensions  map[string]serviceExtension
	engagements map[string]*engagement
	tpVolumes   *xsync.Map[string, *compose.VolumeConfig]
	selectsAll  bool
}

func newTransformer(config *config, p *compose.Project) (tr *transformer, err error) {
	t := &transformer{config: config, project: p, tpVolumes: xsync.NewMap[string, *compose.VolumeConfig](), selectsAll: true}
	t.extensions = make(map[string]serviceExtension)
	for n, sv := range t.project.Services {
		dlog.Debugf(context.Background(), "Service %q has extension %q", n, sv.Extensions)
		ex, ok := sv.Extensions[extensionKey]
		if ok {
			if len(config.services) > 0 && !slices.Contains(config.services, n) {
				t.selectsAll = false
				continue
			}
			eg, err := t.config.parseServiceExtension(&sv, ex)
			if err != nil {
				return nil, err
			}
			t.extensions[n] = eg
		}
	}
	t.engagements = make(map[string]*engagement, len(t.extensions))
	return t, nil
}

func (t *transformer) addEngagement(engagement *engagement) {
	t.engagements[engagement.composeService().Name] = engagement
}

func (t *transformer) createProject(cmdName, composeFile string) ([]string, error) {
	c := t.config
	opts := make([]string, 0, 10)
	opts = append(opts, "compose", "--file", composeFile)

	// Add "compose" specific options
	if c.projectName != "" {
		opts = append(opts, "--project-name", c.projectName)
	}
	if c.projectDir == "" {
		var err error
		c.projectDir, err = os.Getwd()
		if err != nil {
			return nil, errcat.NoDaemonLogs.New(err)
		}
	}
	opts = append(opts, "--project-directory", c.projectDir)
	if c.progress != "auto" {
		opts = append(opts, "--progress", c.progress)
	}
	for _, envFile := range c.envFiles {
		opts = append(opts, "--env-file", envFile)
	}
	opts = append(opts, cmdName)
	opts = c.appendFlags(c.subCommandFlags, opts)
	return opts, nil
}

func (t *transformer) marshalYAML() ([]byte, error) {
	return t.project.MarshalYAML()
}

func (t *transformer) engage(g *dgroup.Group, e serviceExtension, aesCh chan<- *engagement) {
	cn := e.composeService().Name
	g.Go(cn, func(ctx context.Context) (err error) {
		ctx = progress.WithEventId(ctx, cn)
		progress.Workingf(ctx, fmt.Sprintf("%s %s", e.engagementType().Working(), cn))
		var ae *engagement
		if t.config.mustBeConnected {
			ae, err = e.engaged()
		} else {
			ae, err = e.activate(t)
		}
		if err != nil {
			return progress.MaybeWriteError(ctx, err)
		}
		progress.Donef(ctx, fmt.Sprintf("%s %s", e.engagementType().WorkDone(), cn))
		aesCh <- ae
		return nil
	})
}

func (t *transformer) disengage(ctx context.Context) {
	progress.Start(ctx, "Disengaging")
	for _, n := range maps.SortedKeys(t.engagements) {
		e := t.engagements[n]
		eCtx := progress.WithEventId(ctx, n)
		progress.Workingf(eCtx, fmt.Sprintf("%s %s", e.engagementType().Leaving(), n))
		err := e.deactivate()
		if err != nil {
			dlog.Error(eCtx, err)
		}
		progress.Donef(eCtx, fmt.Sprintf("%s %s", e.engagementType().Left(), n))
	}
	progress.Stop(ctx)
}

func (t *transformer) runCommand(ctx context.Context, name string) error {
	forceRecreate := false
	canCreate := t.selectsAll
	if canCreate {
		if f := t.config.subCommandFlags.Lookup("force-recreate"); f != nil && f.Changed {
			forceRecreate = f.Value.String() == "true"
		}
		if f := t.config.subCommandFlags.Lookup("no-recreate"); f != nil && f.Changed {
			canCreate = f.Value.String() != "true"
		}
	}
	composeFile, err := t.createConfigFile(ctx, canCreate, forceRecreate)
	if err != nil {
		return err
	}
	if ep := t.config.existingProject; ep != nil && !t.selectsAll {
		// Verify that all extended services that provide volumes are included.
		teleVols := make(map[string]struct{})
		for n, v := range ep.Volumes {
			if strings.Contains(v.Driver, "/telemount:") {
				teleVols[n] = struct{}{}
			}
		}
		dlog.Debugf(ctx, "teleVols: %v", teleVols)
		for n, sv := range ep.Services {
			ex, ok := sv.Extensions[extensionKey]
			if !ok {
				continue
			}
			if slices.Contains(t.config.services, n) {
				continue
			}
			eg, err := t.config.parseServiceExtension(&sv, ex)
			if err != nil {
				return err
			}
			switch eg.engagementType() {
			case types.EngagementTypeConnect, types.EngagementTypeProxy:
				continue
			default:
			}
			dlog.Debugf(ctx, "Checking if service %q is a volume provider", n)
			if sv.Volumes != nil {
				for _, v := range sv.Volumes {
					if v.Type == compose.VolumeTypeVolume {
						if _, ok := teleVols[v.Source]; ok {
							return errcat.User.Newf("volume %q is provided extended service %q, but that service is not included", v.Source, n)
						}
					}
				}
			}
		}
	}
	opts, err := t.createProject(name, composeFile)
	if err != nil {
		return err
	}
	err = t.runCompose(ctx, name, composeFile, opts)
	if err != nil {
		// Prefer error output from the command to the exit code error.
		err = errcat.Silent.New(err)
	}
	return err
}

func (t *transformer) runCompose(ctx context.Context, name, composeFile string, opts []string) (err error) {
	if name == "up" && !flags.HasOption("detach", 'd', opts) {
		return t.runAttachedUp(ctx, composeFile, opts)
	}
	cmd := proc.StdCommand(ctx, docker.Exe, append(opts, t.config.services...)...)
	cmd.Env = os.Environ()
	return cmd.Run()
}

func (t *transformer) runAttachedUp(parentCtx context.Context, composeFile string, opts []string) (err error) {
	// We need to ensure that containers are stopped when the parentCtx is canceled, but we don't want to do that
	// by killing the "docker compose up" process. There are multiple reasons for this:
	//
	// 1. If the "docker compose up" process is interrupted, it will detach, and the containers will continue to run
	//    for a while longer. We don't want that because some of them might depend on engagements that will end once
	//    this function returns.
	// 2. On windows, the "docker compose up" will detach, but it won't stop the containers at all.s
	ctx := context.WithoutCancel(parentCtx)
	cmd := proc.StdCommand(ctx, docker.Exe, append(opts, t.config.services...)...)
	cmd.Stdin = dos.Stdin(ctx)
	cmd.Env = os.Environ()
	err = cmd.Start()
	if err != nil {
		return err
	}

	parentCtx, parentCancel := context.WithCancel(parentCtx)
	stopDone := make(chan struct{})
	go func() {
		<-parentCtx.Done()
		args := append([]string{"compose", "--file", composeFile, "stop"}, t.config.services...)
		stopCmd := exec.CommandContext(ctx, docker.Exe, args...)
		// Don't assign stdout/stderr. Avoid duplicated output from "compose up" and "compose stop".
		stopCmd.Env = os.Environ()
		dlog.Debug(ctx, shellquote.ShellString(docker.Exe, args))
		_ = stopCmd.Run()
		close(stopDone)
	}()
	err = cmd.Wait()
	parentCancel()
	<-stopDone
	return err
}

func (t *transformer) serviceExtensions() (ses map[string]serviceExtension) {
	return t.extensions
}

func (t *transformer) volumes() *xsync.Map[string, *compose.VolumeConfig] {
	return t.tpVolumes
}

func (t *transformer) withProfiles(profiles []string) error {
	p, err := t.project.WithProfiles(profiles)
	if err == nil {
		t.project = p
	}
	return err
}

// ApplyEngagements ensures that the compose-spec is modified in accordance with the engagements.
func (t *transformer) applyEngagements() error {
	if len(t.engagements) == 0 {
		return nil
	}

	// WithServiceDisabled performs a deepCopy of the project when called without arguments. We
	// want the original Project intact.
	p := t.project.WithServicesDisabled()
	sm := p.Services
	deps := make(map[string][]string)
	for n, e := range t.engagements {
		if s, ok := sm[n]; ok {
			if e.engagementType() == types.EngagementTypeProxy {
				deps[n] = s.GetDependents(p)
				delete(sm, n)
			} else {
				e.engageService(&s)
				sm[n] = s
			}
		}
	}

	for n, e := range t.engagements {
		if proxyDeps, ok := deps[n]; ok {
			e.engageProxyDependents(p, n, proxyDeps)
		}
	}

	for _, e := range t.engagements {
		e.engageProject(p)
	}

	if t.tpVolumes != nil {
		t.tpVolumes.Range(func(k string, v *compose.VolumeConfig) bool {
			p.Volumes[k] = *v
			return true
		})
	}
	err := t.ensureTopLevelExtension(p)
	if err != nil {
		return err
	}
	err = p.CheckContainerNameUnicity()
	if err == nil {
		t.project = p
	}
	return err
}

func (t *transformer) ensureTopLevelExtension(p *compose.Project) error {
	if _, ok := p.Extensions[extensionKey]; ok {
		return nil
	}
	// Marshal the extension to JSON and then unmarshal it back to a map. This is necessary because the
	// extension is a map[string]any and the marshaler used by Docker Compose doesn't support our json-tags.
	js, err := json.Marshal(t.config.topLevelExtension)
	if err != nil {
		return err
	}
	var ms map[string]any
	err = json.Unmarshal(js, &ms, true)
	if err != nil {
		return err
	}
	if p.Extensions == nil {
		p.Extensions = make(map[string]any)
	}
	p.Extensions[extensionKey] = ms
	return nil
}

func (t *transformer) connections() []*connection {
	ccs := t.config.Connections
	cs := make([]*connection, 0, len(ccs))
nextCfg:
	for _, cc := range ccs {
		for _, e := range t.engagements {
			if e.connection().connectionConfig == cc {
				cs = append(cs, e.connection())
				continue nextCfg
			}
		}
	}
	return cs
}

func (t *transformer) createConfigFile(ctx context.Context, canCreate, forceRecreate bool) (composeFile string, err error) {
	cs := t.connections()
	for _, c := range cs {
		ud := daemon.MustGetUserClient(c)
		composeFile = ud.DaemonInfo().ComposeFile
		if composeFile != "" {
			if !forceRecreate {
				return composeFile, nil
			}
			break
		}
	}
	if !canCreate {
		return "", errcat.User.New(`the initial invocation of "compose up" or "compose create" must include all extended services`)
	}

	if composeFile != "" {
		dlog.Debugf(ctx, "Recreating existing compose file %q", composeFile)
	}

	err = t.applyEngagements()
	if err != nil {
		return "", err
	}

	yml, err := t.marshalYAML()
	if err != nil {
		return "", err
	}
	dlog.Debug(ctx, string(yml))

	if composeFile == "" {
		var mcf *os.File
		mcf, err = os.CreateTemp("", "tpc-*.yaml")
		if err != nil {
			return "", err
		}
		composeFile = mcf.Name()
		defer func() {
			if err != nil {
				_ = os.Remove(composeFile)
			}
		}()
		_, err = mcf.Write(yml)
		mcf.Close()
		if err != nil {
			return "", err
		}

		// Save the name of the docker compose file in the daemon info. This ensures that a `docker compose down` is
		// issued if one of the connections is removed. This is necessary because the network represented by the
		// daemon will no longer be available.
		for _, c := range cs {
			ud := daemon.MustGetUserClient(c)
			info := ud.DaemonInfo()
			info.ComposeFile = composeFile
			err = daemon.SaveInfo(ctx, info, ud.DaemonID().InfoFileName())
		}
	} else {
		err = os.WriteFile(composeFile, yml, 0o644)
		if err != nil {
			return "", err
		}
	}
	return composeFile, nil
}
