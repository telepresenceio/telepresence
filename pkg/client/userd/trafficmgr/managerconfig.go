package trafficmgr

import (
	"context"
	"reflect"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

// dropLocalSettings resets every setting in cfg, the client configuration reported by the
// traffic-manager, that names a local program, directory, image or chart. The traffic-manager
// may switch usage reporting off or blank its destination, but not point it elsewhere. The
// rest of cfg is left as it is.
func dropLocalSettings(ctx context.Context, cfg client.Config) {
	def := client.GetDefaultConfig()
	dropSection(ctx, "docker", cfg.Docker(), def.Docker())
	dropSection(ctx, "helm", cfg.Helm(), def.Helm())
	dropSection(ctx, "images", cfg.Images(), def.Images())

	uc, duc := cfg.Usage(), def.Usage()
	if uc.CollectorAddress != "" {
		dropField(ctx, "usage.collectorAddress", &uc.CollectorAddress, duc.CollectorAddress)
	}
	uc.Insecure = duc.Insecure

	ic, dic := cfg.Intercept(), def.Intercept()
	dropField(ctx, "intercept.sshfsPath", &ic.SshfsPath, dic.SshfsPath)
	dropField(ctx, "intercept.mountsRoot", &ic.MountsRoot, dic.MountsRoot)
}

func dropSection[T any](ctx context.Context, name string, section, def *T) {
	if !reflect.DeepEqual(section, def) {
		clog.Debugf(ctx, "Ignoring section %q in the client configuration from the traffic-manager", name)
	}
	*section = *def
}

func dropField(ctx context.Context, name string, field *string, def string) {
	if *field != def {
		clog.Debugf(ctx, "Ignoring setting %q in the client configuration from the traffic-manager", name)
	}
	*field = def
}
