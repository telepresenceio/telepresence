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
	"path/filepath"
	"strings"
	"time"

	//nolint:depguard // This short script has no logging and no Contexts.
	"os/exec"

	"github.com/blang/semver"
)

func Main() error {
	gitDescBytes, err := exec.Command("git", "describe", "--tags", "--match=v*").Output()
	if err != nil {
		return err
	}
	gitDescStr := strings.TrimSuffix(strings.TrimPrefix(string(gitDescBytes), "v"), "\n")
	gitDescVer, err := semver.Parse(gitDescStr)
	if err != nil {
		return err
	}
	gitDescVer.Patch++

	// If an additional arg has been used, we include it in the tag
	if len(os.Args) >= 2 {
		// gitDescVer.Pre[0] contains the number of commits since the last tag and the
		// shortHash with a 'g' appended.  Since the first section isn't relevant,
		// we get the shortHash this way since we don't need that extra information.
		shortHash, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
		if err != nil {
			return err
		}
		if _, err := fmt.Printf("v%d.%d.%d-%s-%s\n", gitDescVer.Major, gitDescVer.Minor, gitDescVer.Patch, os.Args[1], shortHash); err != nil {
			return err
		}
		return nil
	}

	if _, err := fmt.Printf("v%s-%d\n", gitDescVer, time.Now().Unix()); err != nil {
		return err
	}

	return nil
}

func main() {
	if err := Main(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: error: %v\n", filepath.Base(os.Args[0]), err)
		os.Exit(1)
	}
}
