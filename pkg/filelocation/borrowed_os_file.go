// This file is a lightly modified subset of Go 1.15.7's os/file.go.
//
// It is modified to:
//  - Respect WithGOOS and WithUserHomeDir
//  - Have slightly clearer documentation
//  - Not export the functions

// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package filelocation

import (
	"context"
	"errors"
	"os"
)

// UserHomeDir returns the current user's home directory.
//
//  - On Unix, including macOS, it returns the $HOME environment variable.
//
//  - On Windows, it returns the "%USERPROFILE%" environment variable.
//
//  - On Plan 9, it returns the "$home" environment variable.
//
// If the location cannot be determined (for example, $HOME is not defined),
// then it will return an error.
func UserHomeDir(ctx context.Context) (string, error) {
	if untyped := ctx.Value(homeCtxKey{}); untyped != nil {
		return untyped.(string), nil
	}
	env, enverr := "HOME", "$HOME"
	switch goos(ctx) {
	case "windows":
		env, enverr = "USERPROFILE", "%userprofile%"
	case "plan9":
		env, enverr = "home", "$home"
	}
	if v := os.Getenv(env); v != "" {
		return v, nil
	}
	// On some geese the home directory is not always defined.
	switch goos(ctx) {
	case "android":
		return "/sdcard", nil
	case "darwin":
		if goos(ctx) == "arm64" {
			return "/", nil
		}
	}
	return "", errors.New(enverr + " is not defined")
}

// UserCacheDir returns the default root directory to use for user-specific
// cached data. Callers should create their own application-specific
// subdirectory within this one and use that (for example, using
// AppUserCacheDir).
//
//  - On non-Darwin Unix systems, it returns "$XDG_CACHE_HOME" if non-empty, or
//    else "$HOME/.cache".  Specified by:
//    https://specifications.freedesktop.org/basedir-spec/basedir-spec-latest.html
//
//  - On Darwin, it returns "$HOME/Library/Caches".  Specified by:
//    https://developer.apple.com/library/archive/documentation/FileManagement/Conceptual/FileSystemProgrammingGuide/MacOSXDirectories/MacOSXDirectories.html
//
//  - On Windows, it returns "%LocalAppData%" (usually
//    "C:\Users\%USERNAME%\AppData\Local").
//
//  - On Plan 9, it returns "$home/lib/cache".
//
// If the location cannot be determined (for example, $HOME is not defined),
// then it will return an error.
func userCacheDir(ctx context.Context) (string, error) {
	var dir string

	switch goos(ctx) {
	case "windows":
		if untyped := ctx.Value(homeCtxKey{}); untyped != nil {
			return untyped.(string) + `\AppData\Local`, nil
		}
		dir = os.Getenv("LocalAppData")
		if dir == "" {
			return "", errors.New("%LocalAppData% is not defined")
		}

	case "darwin":
		home, err := UserHomeDir(ctx)
		if err != nil {
			return "", err
		}
		dir = home + "/Library/Caches"

	case "plan9":
		home, err := UserHomeDir(ctx)
		if err != nil {
			return "", err
		}
		dir = home + "/lib/cache"

	default: // Unix
		dir = os.Getenv("XDG_CACHE_HOME")
		if dir == "" {
			home, _ := UserHomeDir(ctx)
			if home == "" {
				return "", errors.New("neither $XDG_CACHE_HOME nor $HOME are defined")
			}
			dir = home + "/.cache"
		}
	}

	return dir, nil
}

// UserConfigDir returns the default root directory to use for user-specific
// configuration data. Users should create their own application-specific
// subdirectory within this one and use that (for example, using
// AppUserConfigDir).
//
//  - On non-Darwin Unix systems, it returns "$XDG_CONFIG_HOME" if non-empty, or
//    else "$HOME/.config".  Specified by:
//    https://specifications.freedesktop.org/basedir-spec/basedir-spec-latest.html
//
//  - On Darwin, it returns "$HOME/Library/Application Support".  Specified by:
//    https://developer.apple.com/library/archive/documentation/FileManagement/Conceptual/FileSystemProgrammingGuide/MacOSXDirectories/MacOSXDirectories.html
//    See also: https://github.com/golang/go/commit/b33652642286cf4c3fc8b10cdda97bd58059ba3e
//
//  - On Windows, it returns "%AppData%" (usually "C:\Users\UserName\AppData\Roaming").
//
//  - On Plan 9, it returns "$home/lib".
//
// If the location cannot be determined (for example, $HOME is not defined),
// then it will return an error.
func UserConfigDir(ctx context.Context) (string, error) {
	var dir string

	switch goos(ctx) {
	case "windows":
		if untyped := ctx.Value(homeCtxKey{}); untyped != nil {
			return untyped.(string) + `\AppData\Roaming`, nil
		}
		dir = os.Getenv("AppData")
		if dir == "" {
			return "", errors.New("%AppData% is not defined")
		}

	case "darwin":
		home, err := UserHomeDir(ctx)
		if err != nil {
			return "", err
		}
		dir = home + "/Library/Application Support"

	case "plan9":
		home, err := UserHomeDir(ctx)
		if err != nil {
			return "", err
		}
		dir = home + "/lib"

	default: // Unix
		dir = os.Getenv("XDG_CONFIG_HOME")
		if dir == "" {
			home, _ := UserHomeDir(ctx)
			if home == "" {
				return "", errors.New("neither $XDG_CONFIG_HOME nor $HOME are defined")
			}
			dir = home + "/.config"
		}
	}

	return dir, nil
}
