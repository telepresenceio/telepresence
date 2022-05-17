// Copyright 2021 Datawire. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"fmt"
	"os"

	//nolint:depguard // This short script has no logging and no Contexts.
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/blang/semver"
)

// isReleased returns true if a release tag exist for the given version
// A release tag is a tag that represents a semver version, without pre-version
// or build suffixes, that is prefixed with "v", e.g. "v1.2.3" is considered
// a release tag whereas "v1.2.3-rc.3" isn't.
func isReleased(v semver.Version) bool {
	v.Build = nil
	v.Pre = nil
	return exec.Command("git", "describe", "v"+v.String()).Run() == nil
}

func Main() error {
	cmd := exec.Command("git", "describe", "--tags", "--match=v*")
	cmd.Stderr = os.Stderr
	gitDescBytes, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("unable to git describe: %w", err)
	}
	gitDescStr := strings.TrimSuffix(strings.TrimPrefix(string(gitDescBytes), "v"), "\n")
	gitDescVer, err := semver.Parse(gitDescStr)
	if err != nil {
		return fmt.Errorf("unable to parse semver %s: %w", gitDescStr, err)
	}

	// Bump to next patch version only if the version has been released.
	if isReleased(gitDescVer) {
		gitDescVer.Patch++
	}

	// If an additional arg has been used, we include it in the tag
	if len(os.Args) >= 2 {
		// gitDescVer.Pre[0] contains the number of commits since the last tag and the
		// shortHash with a 'g' appended.  Since the first section isn't relevant,
		// we get the shortHash this way since we don't need that extra information.
		cmd = exec.Command("git", "rev-parse", "--short", "HEAD")
		cmd.Stderr = os.Stderr
		shortHash, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("unable to git rev-parse: %w", err)
		}
		if _, err := fmt.Printf("v%d.%d.%d-%s-%s\n", gitDescVer.Major, gitDescVer.Minor, gitDescVer.Patch, os.Args[1], shortHash); err != nil {
			return fmt.Errorf("unable to printf: %w", err)
		}
		return nil
	}

	if _, err := fmt.Printf("v%s-%d\n", gitDescVer, time.Now().Unix()); err != nil {
		return fmt.Errorf("unable to printf: %w", err)
	}

	return nil
}

func main() {
	if err := Main(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: error: %v\n", filepath.Base(os.Args[0]), err)
		os.Exit(1)
	}
}
