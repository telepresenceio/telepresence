package dns

import (
	"os/exec"
	"strconv"
	"strings"
)

func parseVersion(str string) ([]int, error) {
	strParts := strings.Split(str, ".")
	intParts := make([]int, len(strParts))
	for i := range strParts {
		var err error
		intParts[i], err = strconv.Atoi(strParts[i])
		if err != nil {
			return nil, err
		}
	}
	return intParts, nil
}

// Return values:
//  < 0 : if a < b
//    0 : if a == b
//  > 0 : if a > b
func cmpVersion(a, b []int) int {
	for i := 0; i < len(a) || i < len(b); i++ {
		aPart := 0
		if i < len(a) {
			aPart = a[i]
		}
		bPart := 0
		if i < len(b) {
			bPart = b[i]
		}

		if aPart != bPart {
			return aPart - bPart
		}
	}
	return 0
}

func Flush() {
	output, err := exec.Command("sw_vers", "-productVersion").Output()
	if err != nil {
		return
	}
	verStr := strings.TrimSuffix(string(output), "\n")
	ver, err := parseVersion(verStr)
	if err != nil {
		return
	}

	haveVer := func(minVer ...int) bool {
		return cmpVersion(ver, minVer) >= 0
	}

	// As of macOS 10.14 (Mojave), how to flush the DNS cache
	// hasn't changed since 10.10.4.
	//
	// FIXME: Verify that it hasn't changed in 10.15 (Catalina).
	//
	// References:
	//  - https://support.apple.com/en-us/HT202516 (no longer updated?)
	//  - https://github.com/nbcarey/flush-dns-osx
	//  - https://github.com/eventi/noreallyjustfuckingstopalready
	switch {
	case haveVer(10, 10, 4):
		_ = exec.Command("dscacheutil", "-flushcache").Run()
		_ = exec.Command("killall", "-HUP", "mDNSResponder").Run()
	case haveVer(10, 10):
		_ = exec.Command("discoveryutil", "mdnsflushcache").Run()
		_ = exec.Command("discoveryutil", "udnsflushcache").Run()
	default:
		log("How are we even running?  Go 1.11 requires at least macOS 10.10, but we're on %q", verStr)
	}
}
