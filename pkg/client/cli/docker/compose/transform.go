package compose

import (
	"context"
	"os"

	compose "github.com/compose-spec/compose-go/v2/types"
	"github.com/puzpuzpuz/xsync/v4"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

type transformer struct {
	config      *config
	project     *compose.Project
	extensions  map[string]serviceExtension
	engagements map[string]*engagement
	tpVolumes   *xsync.Map[string, *compose.VolumeConfig]
}

func newTransformer(config *config, p *compose.Project) (tr *transformer, err error) {
	t := &transformer{config: config, project: p, tpVolumes: xsync.NewMap[string, *compose.VolumeConfig]()}
	if len(config.services) > 0 {
		t.project, err = t.project.WithSelectedServices(t.config.services)
		if err != nil {
			return nil, err
		}
	}
	t.extensions = make(map[string]serviceExtension)
	for n, sv := range t.project.Services {
		ex, ok := sv.Extensions[extensionKey]
		if ok {
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

func (t *transformer) createProject(ctx context.Context, cmdName string) ([]string, error) {
	forceRecreate := cmdName == "up" || cmdName == "create"
	composeFile, err := t.createConfigFile(ctx, forceRecreate)
	if err != nil {
		return nil, err
	}
	c := t.config
	opts := make([]string, 0, 10)
	opts = append(opts, "compose", "--file", composeFile)

	// Add "compose" specific options
	if c.projectName != "" {
		opts = append(opts, "--project-name", c.projectName)
	}
	if c.projectDir == "" {
		c.projectDir, err = os.Getwd()
		if err != nil {
			return nil, err
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

func (t *transformer) runCommand(ctx context.Context, name string, args []string) error {
	opts, err := t.createProject(ctx, name)
	if err != nil {
		return err
	}
	err = proc.StdCommand(ctx, "docker", append(opts, args...)...).Run()
	if err != nil {
		// Prefer error output from the command to the exit code error.
		err = errcat.Silent.New(err)
	}
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
	js, err := client.MarshalJSON(t.config.topLevelExtension)
	if err != nil {
		return err
	}
	var ms map[string]any
	err = client.UnmarshalJSON(js, &ms, true)
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

func (t *transformer) createConfigFile(ctx context.Context, forceRecreate bool) (composeFile string, err error) {
	conns := t.connections()
	for _, c := range conns {
		ud := daemon.GetUserClient(c)
		composeFile = ud.DaemonInfo().ComposeFile
		if composeFile != "" {
			if !forceRecreate {
				return composeFile, nil
			}
			break
		}
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
		for _, c := range conns {
			ud := daemon.GetUserClient(c)
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
