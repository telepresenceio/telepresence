package docker

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/pflag"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/flags"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/progress"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

type Flags struct {
	Run          bool     // --docker-run
	Debug        bool     // set if --docker-debug was used
	BuildOptions []string // --docker-build-opt key=value, // Optional flag to docker build can be repeated (but not comma separated)
	Context      string   // Set to build or debug by Validate function
	Image        string
	Mount        string // --docker-mount // where to mount in a docker container. Defaults to mount unless mount is "true" or "false".
	build        string // --docker-build DIR | URL
	debug        string // --docker-debug DIR | URL
	args         []string
	imageIndex   int
}

func (f *Flags) AddFlags(flagSet *pflag.FlagSet, what string) {
	flagSet.BoolVar(&f.Run, "docker-run", false, fmt.Sprintf(
		`Run a Docker container with %s environment, volume mount, by passing arguments after -- to 'docker run', `+
			`e.g. '--docker-run -- -it --rm ubuntu:20.04 /bin/bash'`, what))

	flagSet.StringVar(&f.build, "docker-build", "", fmt.Sprintf(
		`Build a Docker container from the given docker-context (path or URL), and run it with %s environment and volume mounts, `+
			`by passing arguments after -- to 'docker run', e.g. '--docker-build /path/to/docker/context -- -it IMAGE /bin/bash'`, what))

	flagSet.StringVar(&f.debug, "docker-debug", "", ``+
		`Like --docker-build, but allows a debugger to run inside the container with relaxed security`)

	flagSet.StringArrayVar(&f.BuildOptions, "docker-build-opt", nil,
		`Options to docker-build in the form key=value, e.g. --docker-build-opt tag=mytag.`)

	flagSet.StringVar(&f.Mount, "docker-mount", "", ``+
		`The volume mount point in docker. Defaults to same as "--mount"`)
}

func (f *Flags) Validate(args []string) error {
	drCount := 0
	if f.Run {
		drCount++
	}
	if f.build != "" {
		drCount++
		f.Context = f.build
	}
	if f.debug != "" {
		drCount++
		f.Context = f.debug
		f.Debug = true
	}
	alts := "--docker-run, --docker-build, or --docker-debug"
	if drCount > 1 {
		return errcat.User.Newf("only one of %s can be used", alts)
	}
	f.Run = drCount == 1
	if !f.Run {
		if f.Mount != "" {
			return errcat.User.Newf("--docker-mount must be used together with %s", alts)
		}
		return nil
	}

	if flags.HasOption("detach", 'd', args) {
		return errcat.User.New("running docker container in background using -d or --detach is not supported")
	}
	f.Image, f.imageIndex = firstArg(args)
	f.args = args

	// Ensure that the image is ready to run before we create the intercept.
	if f.Context == "" {
		if f.imageIndex < 0 {
			return errcat.User.New(`unable to find the image name. When using --docker-run, the syntax after "--" must be [OPTIONS] IMAGE [COMMAND] [ARG...]`)
		}
		if f.Image != "IMAGE" {
			return nil
		}
	}
	f.Image = ""
	if f.imageIndex < 0 && len(args) > 0 {
		return errcat.User.New(`` +
			`the string "IMAGE", acting as a placeholder for image ID, must be included after "--" when using "--docker-build", so ` +
			`that flags intended for docker run can be distinguished from the command and arguments intended for the container.`)
	}
	return nil
}

// PullOrBuildImage will pull or build the image and return the args list suitable
// when starting it.
func (f *Flags) PullOrBuildImage(ctx context.Context) error {
	if f.Image != "" {
		return docker.PullImage(ctx, f.Image)
	}
	opts := make([]string, len(f.BuildOptions))
	for i, opt := range f.BuildOptions {
		opts[i] = "--" + opt
	}
	progress.Working(ctx, "Building")
	imageID, err := docker.BuildImage(ctx, f.Context, opts)
	if err != nil {
		return progress.MaybeWriteError(ctx, err)
	}
	progress.Done(ctx, "Built")
	if f.imageIndex < 0 {
		f.args = []string{imageID}
		f.imageIndex = 0
	} else {
		f.args[f.imageIndex] = imageID
	}
	return nil
}

func (f *Flags) GetContainerNameAndArgs(defaultContainerName string) (string, []string, error) {
	name, found, err := flags.GetUnparsedValue("name", 0, false, f.args)
	if err != nil {
		return "", nil, err
	}
	if !found {
		name = defaultContainerName
		f.args = append([]string{"--name", name}, f.args...)
		f.imageIndex += 2
	}
	return name, f.args, nil
}

func (f *Flags) AdjustImageIndex(adjust int) {
	if f.imageIndex >= 0 {
		f.imageIndex += adjust
	}
}

var boolFlags = map[string]bool{ //nolint:gochecknoglobals // this is a constant
	"--detach":           true,
	"--init":             true,
	"--interactive":      true,
	"--no-healthcheck":   true,
	"--oom-kill-disable": true,
	"--privileged":       true,
	"--publish-all":      true,
	"--quiet":            true,
	"--read-only":        true,
	"--rm":               true,
	"--sig-proxy":        true,
	"--tty":              true,
}

// firstArg returns the first argument that isn't an option. This requires knowledge
// about boolean docker flags, and if new such flags arrive and are used, this
// function might return an incorrect image.
func firstArg(args []string) (string, int) {
	t := len(args)
	for i := 0; i < t; i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			return arg, i
		}
		if strings.IndexByte(arg, '=') > 0 {
			continue
		}
		if strings.HasPrefix(arg, "--") {
			if !boolFlags[arg] {
				i++
			}
		} else if strings.ContainsAny(arg, "ehlmpuvw") {
			// Shorthand flag that require an argument. Might be prefixed by shorthand booleans, e.g. -itl <label>
			i++
		}
	}
	return "", -1
}
