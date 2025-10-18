package global

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

const (
	FlagConfig   = "config"
	FlagContext  = "context"
	FlagDocker   = "docker"
	FlagNoReport = "no-report"
	FlagOutput   = "output"
	FlagProgress = "progress"
	FlagUse      = "use"
)

var FlagNames = []string{FlagContext, FlagDocker, FlagNoReport, FlagOutput, FlagProgress, FlagUse} //nolint:gochecknoglobals // constant names

func replaceHomeDir(ctx context.Context, dir string) string {
	homeEnv := "$HOME"
	if runtime.GOOS == "windows" {
		homeEnv = "%USERPROFILE%"
	}
	return strings.Replace(dir, filelocation.UserHomeDir(ctx), homeEnv, 1)
}

func Flags(ctx context.Context, hasKubeFlags, markdown bool) *pflag.FlagSet {
	flags := pflag.NewFlagSet("", 0)
	if !hasKubeFlags {
		// Add deprecated global connect and docker flags.
		flags.String(FlagContext, "", "")
		flags.Lookup(FlagContext).Hidden = true
		flags.Bool(FlagDocker, false, "")
		flags.Lookup(FlagDocker).Hidden = true
	}
	flags.Bool(FlagNoReport, false, "")
	f := flags.Lookup(FlagNoReport)
	f.Hidden = true
	f.Deprecated = "not used"
	flags.String(FlagUse, "", "Match expression that uniquely identifies the daemon container")
	flags.String(FlagOutput, "default", "Set the output format, supported values are 'json', 'yaml', and 'default'")
	flags.String(FlagProgress, "auto", `Set type of progress output (auto, tty, plain, json, quiet)`)
	appDir := filelocation.AppUserConfigDir(ctx)
	if markdown {
		appDir = replaceHomeDir(ctx, appDir)
	}
	flags.String(FlagConfig, filepath.Join(appDir, client.ConfigFile), `Path to the Telepresence configuration file`)
	return flags
}

func SetProgressQuiet(cmd *cobra.Command) {
	pf := cmd.Flag(FlagProgress)
	_ = pf.Value.Set("quiet")
	pf.Changed = true
}

func InitConfig(cmd *cobra.Command) error {
	if configFile := cmd.Flags().Lookup(FlagConfig).Value.String(); configFile != "" {
		ctx := client.WithConfigFile(cmd.Context(), configFile)
		cfg, err := client.LoadConfig(ctx)
		if err != nil {
			return err
		}
		if client.ReplaceConfig(ctx, cfg) {
			client.ReloadDaemonLogLevel(ctx)
		}
		cmd.SetContext(ctx)
	}
	return nil
}
