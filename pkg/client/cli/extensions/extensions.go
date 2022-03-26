package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/pflag"
	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

type ExtensionsState struct {
	// Data that is static after initialization
	ext2file map[string]string        // initialized in step 1
	exts     map[string]ExtensionInfo // initialized in step 2
	mech2ext map[string]string        // initialized in step 3

	// Stateful data
	flags           *pflag.FlagSet // initialized in step 4
	cachedMechanism struct {
		Mech string
		Err  error
	}
	cachedImage struct {
		Image string
		Err   error
	}
}

// LoadExtensions loads any extension YAML files, and adds the appropriate flags to existingFlags.
//
// Extension YAML files are loaded from the "extensions/" subdirectories in
// filelocation.AppUserConfigDir and filelocation.AppSystemConfigDirs (eg: on GNU/Linux:
// "~/.config/telepresence/extensions/" and "/etc/xdg/telepresence/extensions/").  Files are ignored
// if they begin with "."  or if they don't end with ".yml".  Files with the same name in multiple
// directories will mask eachother (eg: "~/.config/telepresence/extensions/foo.yml" will mask
// "/etc/xdg/telepresence/extensions/foo.yml").
//
// The basename of the extension YAML filename (i.e. the non-directory part, with the ".yml" suffix
// removed) identifies the name of the extension.  The content of the extension YAML file must be an
// ExtensionInfo object serialized as YAML.  See the docs for ExtensionInfo for more information.
func LoadExtensions(ctx context.Context, existingFlags *pflag.FlagSet) (es *ExtensionsState, err error) {
	defer func() {
		// Consider all errors issued here to belong to the Config category.
		if err != nil {
			err = errcat.Config.New(err)
		}
	}()
	es = &ExtensionsState{
		ext2file: make(map[string]string),
		exts:     make(map[string]ExtensionInfo),
		mech2ext: make(map[string]string),
		flags:    existingFlags,
	}

	// 0. (1-2) Pre-load builtin extensions ////////////////////////////////
	for extname, extdata := range builtinExtensions(ctx) {
		es.ext2file[extname] = "<builtin>"
		es.exts[extname] = extdata
	}

	// 1. Scan for extension files to load (es.ext2filename) ///////////////

	userDir, err := filelocation.AppUserConfigDir(ctx)
	if err != nil {
		return nil, err
	}
	systemDirs, err := filelocation.AppSystemConfigDirs(ctx)
	if err != nil {
		return nil, err
	}
	// Iterate over the directories from highest-precedence to lowest-precedence.
	for _, dir := range append([]string{userDir}, systemDirs...) {
		fileinfos, err := os.ReadDir(filepath.Join(dir, "extensions"))
		if err != nil {
			continue
		}
		for _, fileinfo := range fileinfos {
			if strings.HasPrefix(fileinfo.Name(), ".") {
				continue
			}
			if !strings.HasSuffix(fileinfo.Name(), ".yml") {
				continue
			}
			extname := strings.TrimSuffix(fileinfo.Name(), ".yml")

			// Avoid overwriting files from higher-precedence directories.
			if _, masked := es.ext2file[extname]; masked {
				continue
			}

			es.ext2file[extname] = filepath.Join(dir, "extensions", fileinfo.Name())
		}
	}

	// 2. Load selected files (es.exts) ////////////////////////////////////

	// Do this in a deterministic order, so that any error message is consistent.
	extnames := make([]string, 0, len(es.ext2file))
	for extname := range es.ext2file {
		extnames = append(extnames, extname)
	}
	sort.Strings(extnames)
	for _, extname := range extnames {
		if _, builtin := es.exts[extname]; builtin {
			continue
		}
		filename := es.ext2file[extname]

		bs, err := os.ReadFile(filename)
		if err != nil {
			return nil, err
		}
		var extdata ExtensionInfo
		if err := yaml.UnmarshalStrict(bs, &extdata); err != nil {
			return nil, fmt.Errorf("%q: %w", filename, err)
		}
		es.exts[extname] = extdata
	}

	// 3. Check loaded files for consistency (es.mech2ext) /////////////////

	// Likewise, do this in a deterministic order, so that error messages are consistent.

	// First, check for exact-clashes.
	for _, extname := range extnames {
		extdata := es.exts[extname]
		// Likewise, sorted
		mechnames := make([]string, 0, len(extdata.Mechanisms))
		for mechname := range extdata.Mechanisms {
			mechnames = append(mechnames, mechname)
		}
		sort.Strings(mechnames)
		for _, mechname := range mechnames {
			if otherExtname, conflict := es.mech2ext[mechname]; conflict {
				return nil, fmt.Errorf("extension mechanism %q is defined by both %q (%q) and %q (%q)",
					mechname,
					otherExtname, es.ext2file[otherExtname],
					extname, es.ext2file[extname])
			}
			es.mech2ext[mechname] = extname
		}
	}
	// Second, check for prefix-clashes (likewise, sorted).
	mechnames := make([]string, 0, len(es.mech2ext))
	for mechname := range es.mech2ext {
		mechnames = append(mechnames, mechname)
	}
	sort.Strings(mechnames)
	illegalPrefixes := []string{
		"mechanism",
	}
	existingFlags.VisitAll(func(flag *pflag.Flag) {
		illegalPrefixes = append(illegalPrefixes, strings.SplitN(flag.Name, "-", 2)[0])
	})
	sort.Strings(illegalPrefixes)
	for _, a := range mechnames {
		for _, b := range mechnames {
			if strings.HasPrefix(a, b+"-") {
				return nil, fmt.Errorf("extension mechanism %q (%q): clashes with mechanism %q (%q)",
					a, es.ext2file[es.mech2ext[a]],
					b, es.ext2file[es.mech2ext[b]])
			}
		}
		for _, b := range illegalPrefixes {
			switch {
			case a == b:
				return nil, fmt.Errorf("extension mechanism %q (%q): illegal mechanism name %q",
					a, es.ext2file[es.mech2ext[a]],
					b)
			case strings.HasPrefix(a, b+"-"):
				return nil, fmt.Errorf("extension mechanism %q (%q): illegal mechanism name prefix %q",
					a, es.ext2file[es.mech2ext[a]],
					b+"-")
			}
		}
	}

	// 4. Initialize the CLI flags (es.flags) //////////////////////////////
	es.flags.String("mechanism", es.defaultMechanism(ctx), "Which extension `mechanism` to use")
	// Likewise, do this in a deterministic order, but this time so that the `--help` text is in
	// a consistent order.
	for _, mechname := range mechnames {
		mechdata := es.exts[es.mech2ext[mechname]].Mechanisms[mechname]
		for flagname, flagdata := range mechdata.Flags {
			val, err := flagdata.Type.NewFlagValueFromJson(flagdata.Default)
			if err != nil {
				return nil, fmt.Errorf("extension mechanism %q (%q): flag %q: invalid default for type: %w",
					mechname, es.ext2file[es.mech2ext[mechname]],
					flagname,
					err)
			}
			usage := ""
			if flagdata.Usage != "" {
				usage = fmt.Sprintf(`%s (implies "--mechanism=%s")`, flagdata.Usage, mechname)
			}
			flag := es.flags.VarPF(val, mechname+"-"+flagname, "", usage)
			flag.Hidden = usage == ""
			flag.Deprecated = flagdata.Deprecated
		}
	}

	return es, nil
}

func (es *ExtensionsState) defaultMechanism(ctx context.Context) string {
	type prefData struct {
		preference int
		name       string
	}
	canAPIKey := cliutil.HasLoggedIn(ctx)
	var preferences []prefData
	for _, extdata := range es.exts {
		if extdata.RequiresAPIKeyOrLicense && !canAPIKey {
			continue
		}
		for mechname, mechdata := range extdata.Mechanisms {
			preferences = append(preferences, prefData{
				preference: mechdata.Preference,
				name:       mechname,
			})
		}
	}
	sort.Slice(preferences, func(i, j int) bool {
		switch {
		case preferences[i].preference < preferences[j].preference:
			return true
		case preferences[i].preference > preferences[j].preference:
			return false
		default:
			return preferences[i].name < preferences[j].name
		}
	})
	return preferences[len(preferences)-1].name
}

func (es *ExtensionsState) Mechanism() (string, error) {
	if es.cachedMechanism.Mech != "" || es.cachedMechanism.Err != nil {
		return es.cachedMechanism.Mech, es.cachedMechanism.Err
	}
	mechanisms := make(map[string]string)
	if flag := es.flags.Lookup("mechanism"); flag.Changed {
		mechanisms[flag.Value.String()] = "--mechanism"
	}
	for _, extdata := range es.exts {
		for mechname, mechdata := range extdata.Mechanisms {
			for flagname := range mechdata.Flags {
				flag := es.flags.Lookup(mechname + "-" + flagname)
				if flag.Changed {
					mechanisms[mechname] = "--" + mechname + "-" + flagname
					break
				}
			}
		}
	}

	switch len(mechanisms) {
	case 0:
		es.cachedMechanism.Mech = es.flags.Lookup("mechanism").Value.String()
	case 1:
		for mechname := range mechanisms {
			es.cachedMechanism.Mech = mechname
		}
	default:
		mechStrs := make([]string, 0, len(mechanisms))
		flagStrs := make([]string, 0, len(mechanisms))
		for mechname, flagname := range mechanisms {
			mechStrs = append(mechStrs, mechname)
			flagStrs = append(flagStrs, flagname)
		}
		sort.Strings(mechStrs)
		sort.Strings(flagStrs)
		es.cachedMechanism.Err = fmt.Errorf("different flags (%v) request conflicting mechanisms (%v)",
			flagStrs, mechStrs)
	}

	return es.cachedMechanism.Mech, es.cachedMechanism.Err
}

func (es *ExtensionsState) RequiresAPIKeyOrLicense() (bool, error) {
	mechname, err := es.Mechanism()
	if err != nil {
		return false, err
	}
	return es.exts[es.mech2ext[mechname]].RequiresAPIKeyOrLicense, nil
}

func urlSchemeIsOneOf(urlStr string, schemes ...string) bool {
	u, err := url.Parse(urlStr)
	if err != nil {
		return false
	}
	for _, scheme := range schemes {
		if u.Scheme == scheme {
			return true
		}
	}
	return false
}

// AgentImage returns the repository/name combination that will be assigned to the container
// image attribute.
func (es *ExtensionsState) AgentImage(ctx context.Context) (string, error) {
	cfg := client.GetConfig(ctx)
	if ai := cfg.Images.AgentImage(ctx); ai != "" {
		return fmt.Sprintf("%s/%s", cfg.Images.Registry(ctx), ai), nil
	}
	if es.cachedImage.Image != "" || es.cachedImage.Err != nil {
		return es.cachedImage.Image, es.cachedImage.Err
	}
	mechname, err := es.Mechanism()
	if err != nil {
		return "", err
	}
	image := os.Expand(es.exts[es.mech2ext[mechname]].Image, client.GetEnv(ctx).Get)
	if cfg.Cloud.SkipLogin {
		msg := fmt.Sprintf(
			`images.agentImage must be set with cloud.skipLogin in
%s for intercepts of mechanism: %s`, client.GetConfigFile(ctx), mechname)
		err := errcat.Config.New(msg)
		return "", err
	}
	for urlSchemeIsOneOf(image, "http", "https", "grpc+https") {
		if strings.HasPrefix(strings.ToLower(image), "grpc+") {
			image, err = systemaGetPreferredAgentImageName(ctx, image)
		} else {
			image, err = func() (string, error) {
				req, err := http.NewRequest(http.MethodGet, image, nil)
				if err != nil {
					return "", err
				}
				req = req.WithContext(ctx)

				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					return "", err
				}
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					return "", errcat.NoDaemonLogs.Newf("image URL %q returned HTTP %v", image, resp.StatusCode)
				}
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					return "", errcat.NoDaemonLogs.New(err)
				}
				return strings.TrimSpace(string(body)), nil
			}()
		}
		if err != nil {
			es.cachedImage.Err = err
			return "", err
		}
	}
	es.cachedImage.Image = image
	return image, nil
}

func (es *ExtensionsState) MechanismArgs() ([]string, error) {
	mechname, err := es.Mechanism()
	if err != nil {
		return nil, err
	}
	mechdata := es.exts[es.mech2ext[mechname]].Mechanisms[mechname]

	var args []string
	for flagname := range mechdata.Flags {
		flag := es.flags.Lookup(mechname + "-" + flagname)
		args = append(args, flag.Value.(cliutil.Value).AsArgs(flagname)...)
	}

	return args, nil
}

// ExtensionInfo is the type that the data in an extension YAML file must be.
type ExtensionInfo struct {
	// Image is the agent image name to install as a sidecar in order to use this extension.
	//
	// Alternatively, instead of a Docker image name, you may give an "http://", https://", or
	// "grpc+https://" URL.  For an "http://" or "https://" URL, the URL must return an HTTP 200
	// response where the response body will be used as the Docker image name.  For a
	// "grpc+https://" url, it will make a `/telepresence.systema/PreferredAgentResponse`
	// request to the server specified in the URL.  This is done recursively.
	//
	// The initial string has environment variables expanded via os.Expand.  Strings returned
	// from HTTP or gRPC requests do not have environment variables expanded.
	Image string `json:"image"`
	// RequiresAPIKeyOrLicense identifies whether the agent sidecar image requires a SystemA API key (via
	// `telepresence login`) or a license in the cluster in order to function.
	RequiresAPIKeyOrLicense bool `json:"requiresAPIKeyOrLicense,omitempty"`
	// Mechanisms describes the mechanisms that the agent sidecar image supports.  The keys in
	// the map are the mechanism names.
	Mechanisms map[string]MechanismInfo `json:"mechanisms"`
}

// MechanismInfo is the information about a mechanism in ExtensionInfo.
type MechanismInfo struct {
	// Preference identifies an ordering of preference for choosing the default mechanism if not
	// told explicitly via a flag.  The highest preference mechanism will be used as the
	// default; with the exception that the mechanism(s) of a requiresAPIKeyOrLicense extension will not
	// be considered if not logged in or if you cannot access the cloud and use a license.
	// Ties are decided by lexicographic ordering.
	Preference int `json:"preference,omitempty"`

	// Flags describes which CLI flags this mechanism introduces to `telepresence intercept`.
	// The flag will be exposed to the user as `--{{mechname}}-{{mapkey}}`, and will be passed
	// to the agent sidecar gRPC responses as `--{{mapkey}}`.
	Flags map[string]FlagInfo `json:"flags"`
}

type FlagInfo struct {
	// Usage is the usage text for the flag to include in `--help` output.  It follows pflag
	// semantics around the backtick character being used to identify meta-variables.  Strictly
	// speaking, this field is optional, but you should probably include it.
	Usage string `json:"usage"`
	// Type identifies the type identifies the datatype to use both for (1) parsing the default
	// value of a flag (below), and for (2) validating and normalizing the flag value that the
	// user passes on the CLI.  See the `flagTypes` variable in `flagtypes.go` for a list of
	// possible values.  This field is required.
	Type cliutil.TypeEnum `json:"type"`
	// Default is the default value for this flag.  This field is optional; if it isn't
	// specified then the zero value is used.
	Default json.RawMessage `json:"default,omitempty"`

	// Deprecated is set if the flag is deprecated in favor of something else. Deprecation
	// means that the flag retains its original function, is hidden from help, and that using it will
	// display this field as a warning.
	Deprecated string `json:"deprecated,omitempty"`
}
