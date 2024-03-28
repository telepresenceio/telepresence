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
	"crypto/md5"
	"encoding/base64"
	"fmt"
	"io/fs"
	"os"
	//nolint:depguard // This short script has no logging and no Contexts.
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/blang/semver"
	ignore "github.com/sabhiram/go-gitignore"
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

// dirMD5 computes the MD5 checksum of all files found when recursively
// traversing a directory, skipping .gitignore's, _test/, and _test.go. The
// general idea is to avoid rebuilds and pushes when repeatedly running tests,
// even if the tests themselves actually change.
func dirMD5(root string) ([]byte, error) {
	ign, err := ignore.CompileIgnoreFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		return nil, err
	}
	d := md5.New()
	testMach := fmt.Sprintf("_test%c", filepath.Separator)
	isTest := func(path string) bool {
		return strings.Contains(path, testMach) || strings.HasSuffix(path, "_test.go")
	}
	err = filepath.Walk(root, func(path string, info fs.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() && !(ign.MatchesPath(path) || isTest(path)) {
			var data []byte
			if data, err = os.ReadFile(path); err == nil {
				d.Write(data)
			}
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return d.Sum(make([]byte, 0, md5.Size)), nil
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

	// Append a mangled md5 if the directory is dirty.
	cmd = exec.Command("git", "status", "--short")
	cmd.Stderr = os.Stderr
	statusBytes, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("unable to git rev-parse: %w", err)
	}
	if len(statusBytes) > 0 {
		var md5Out []byte
		md5Out, err = dirMD5(".")
		if err != nil {
			return fmt.Errorf("unable to compute MD5: %w", err)
		}

		b64 := base64.RawURLEncoding.EncodeToString(md5Out)
		b64 = strings.ReplaceAll(b64, "_", "Z")
		b64 = strings.ReplaceAll(b64, "-", "z")
		_, err = fmt.Printf("v%s-%s\n", gitDescVer, b64)
	} else {
		_, err = fmt.Printf("v%s\n", gitDescVer)
	}
	return err
}

func main() {
	if err := Main(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: error: %v\n", filepath.Base(os.Args[0]), err)
		os.Exit(1)
	}
}
