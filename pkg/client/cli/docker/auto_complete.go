package docker

import (
	"bytes"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client/docker"
)

var directiveCodeRx = regexp.MustCompile(`^:(\d)$`) //nolint:gochecknoglobals // constant

func AutocompleteRun(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	args = slices.Insert(args, 0, "__completeNoDesc", "run")
	args = append(args, toComplete)
	cc := exec.CommandContext(cmd.Context(), docker.Exe, args...)
	cc.Env = os.Environ()
	ob := bytes.Buffer{}
	cc.Stdout = &ob
	if err := cc.Run(); err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	names := strings.Fields(ob.String())
	if ln := len(names) - 1; ln >= 0 {
		// The last name is the directive in the form of a colon followed by a digit.
		if m := directiveCodeRx.FindStringSubmatch(names[ln]); m != nil {
			n, _ := strconv.Atoi(m[1])
			return names[:ln], cobra.ShellCompDirective(n)
		}
	}
	return nil, cobra.ShellCompDirectiveError
}
