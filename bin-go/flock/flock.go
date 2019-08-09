// Copyright 2018 Datawire. All rights reserved.

// flock.go is a minimal implementation of flock(1) (from util-linux)
// for systems that don't have flock(1) but do have flock(2).
//
// According to the Linux "man-pages"[1] flock(2) documentation:
//
//     CONFORMING TO
//            4.4BSD (the flock() call first appeared in 4.2BSD).  A
//            version of flock(), possibly implemented in terms of
//            fcntl(2), appears on most UNIX systems.
//
// At the very least, macOS has flock(2).
//
// [1]: https://www.kernel.org/doc/man-pages/
//
// No, BSD/macOS shlock(1) is not an accepable substitute.  It is the
// opposite of robust.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"syscall"
)

var program_invocation_short_name = path.Base(os.Args[0])

func usage() {
	fmt.Printf("Usage: %s FILE|DIRECTORY COMMAND [ARGUMENTS]\n", program_invocation_short_name)
	fmt.Printf("Manage file locks from shell scripts.\n")
	fmt.Printf("\n")
	fmt.Printf("This implementation does not support any options.\n")
	fmt.Printf("This implementation does not support other usages.\n")
	os.Exit(0)
}

func exit(err error) {
	fmt.Fprintf(os.Stderr, "%s: %v\n", program_invocation_short_name, err)
	os.Exit(1)
}

func errusage(format string, a ...interface{}) {
	if format != "" {
		fmt.Fprintf(os.Stderr, "%s: %s\n", program_invocation_short_name, fmt.Sprintf(format, a...))
	}
	fmt.Fprintf(os.Stderr, "Try '%s --help' for more information.\n", program_invocation_short_name)
	os.Exit(64)
}

func main() {
	if len(os.Args) < 3 {
		if len(os.Args) > 1 {
			if os.Args[1] == "-h" || os.Args[1] == "--help" {
				usage()
			}
			if strings.HasPrefix(os.Args[1], "-") {
				errusage("invalid option: %q", os.Args[1])
			}
		}
		errusage("not enough arguments")
	}

	file, err := os.OpenFile(os.Args[1], os.O_RDONLY|os.O_CREATE, 0666)
	if pe, ok := err.(*os.PathError); ok && pe.Err == syscall.EISDIR {
		file, err = os.OpenFile(os.Args[1], os.O_RDONLY, 0666)
	}
	if err != nil {
		exit(err)
	}

	err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
	if err != nil {
		err = &os.PathError{Op: "flock", Path: file.Name(), Err: err}
		exit(err)
	}

	cmd := exec.Command(os.Args[2], os.Args[3:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ProcessState.Sys().(syscall.WaitStatus).ExitStatus())
		}
		exit(err)
	}
	file.Close()
}
