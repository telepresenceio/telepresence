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

// +build ignore

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pkg/browser"
)

func Main() error {
	cmd := exec.Command("git", "branch", "--show-current")
	cmd.Stderr = os.Stderr
	branchBytes, err := cmd.Output()
	if err != nil {
		return err
	}
	branch := strings.TrimSpace(string(branchBytes))

	cmd = exec.Command("git", "push", "origin", branch)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}

	// 1. Compare against 'release/v2' instead of 'master'
	// 2. Set the PR template to 'v2.md' instead of the default 'pull_request_template.md'
	url := fmt.Sprintf("https://github.com/telepresenceio/telepresence/compare/release/v2...%s?expand=1&template=v2.md", branch)

	fmt.Printf("Opening URL: %s\n", url)
	if err := browser.OpenURL(url); err != nil {
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
