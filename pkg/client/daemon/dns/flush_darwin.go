package dns

import (
	"context"
	"os"
	"os/exec" //nolint:depguard // don't want context or logging
	"regexp"
	"strconv"
	"strings"

	"github.com/datawire/dlib/dlog"
)

var devNullReader *os.File
var devNullWriter *os.File
var majorProductVersion int

func init() {
	var err error
	devNullReader, err = os.Open(os.DevNull)
	if err != nil {
		panic(err)
	}
	devNullWriter, err = os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		panic(err)
	}
	if swOut, err := exec.Command("sw_vers", "-productVersion").Output(); err == nil {
		if m := regexp.MustCompile(`^(\d+)\.`).FindStringSubmatch(strings.TrimSpace(string(swOut))); m != nil {
			majorProductVersion, _ = strconv.Atoi(m[1])
		}
	}
}

func devNullCmd(c context.Context, executable string, args ...string) {
	// Run the command without having the OS bother about opening or closing file descriptors or pipes
	cmd := exec.CommandContext(c, executable, args...)
	cmd.Stdin = devNullReader
	cmd.Stdout = devNullWriter
	cmd.Stderr = devNullWriter
	_ = cmd.Run()
}

func Flush(c context.Context) {
	// As of macOS 11 (Big Sur), how to flush the DNS cache hasn't changed since 10.10.4 (a Yosemite version release in mid 2015),
	// other than that in 10.12 (Sierra) it became necessary to also kill mDNSResponderHelper. On older versions the call to kill
	// mDNSResponderHelper is unnecessary but harmless, as the process doesn't exist.
	dlog.Debug(c, "Flushing DNS")
	if majorProductVersion >= 11 {
		// Big Sur, a killall -HUP mDNSResponder doesn't restart the mDNSResponderHelper but killall -TERM of both, does`
		dlog.Debug(c, "Flushing DNS")
		devNullCmd(c, "killall", "-HUP", "mDNSResponder")
	} else {
		dlog.Debug(c, "Flushing DNS on version < Big Sur")
		devNullCmd(c, "killall", "mDNSResponderHelper")
		devNullCmd(c, "killall", "-HUP", "mDNSResponder", "mDNSResponderHelper")
	}
	devNullCmd(c, "dscacheutil", "-flushcache")
}
