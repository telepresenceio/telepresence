// +build ignore

package main

import (
	"crypto/md5"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

var Verbose bool

type Environ map[string]string
type Config struct {
	Unversioned []string
	Profiles    map[string]Environ
}

func die(err error, args ...interface{}) {
	if err != nil {
		if args != nil {
			panic(fmt.Errorf("%v: %v", err, args))
		} else {
			panic(err)
		}
	}
}

func main() {
	verb := os.Getenv("VERBOSE")
	if verb != "" {
		var err error
		Verbose, err = strconv.ParseBool(verb)
		if err != nil {
			fmt.Printf("warning: unable to parse VERBOSE=%s as bool\n", verb)
		}
	}

	var profile = flag.String("profile", "dev", "profile")
	var output = flag.String("output", "", "output file")
	var input = flag.String("input", "", "input file")
	var newline = flag.String("newline", "\n", "string to use for newline")
	flag.Parse()

	var err error
	var in *os.File
	if *input != "" {
		in, err = os.Open(*input)
		defer in.Close()
	} else {
		in = os.Stdin
	}

	var out *os.File
	if *output != "" {
		out, err := os.Create(*output)
		die(err)
		defer out.Close()
	} else {
		out = os.Stdout
	}

	bytes, err := ioutil.ReadAll(in)
	die(err)

	var config Config
	err = json.Unmarshal(bytes, &config)
	die(err)

	current, ok := config.Profiles[*profile]
	if !ok {
		panic("no such profile: " + *profile)
	}

	out.WriteString(fmt.Sprintf("PROFILE=%s%s", *profile, *newline))

	combined := make(map[string]string)

	for k, v := range config.Profiles["default"] {
		combined[k] = v
	}
	for k, v := range current {
		combined[k] = v
	}

	for k, v := range combined {
		out.WriteString(fmt.Sprintf("%s=%s%s", k, v, *newline))
	}

	out.WriteString(fmt.Sprintf("HASH=%x%s", hash(config.Unversioned), *newline))
}

func versioned(path string, excludes []string) bool {
	for _, ex := range excludes {
		m, err := filepath.Match(ex, path)
		die(err)
		if m {
			return false
		}
	}
	return true
}

func hash(unversioned []string) []byte {
	standard, err := shell("git ls-files --exclude-standard")
	die(err)
	others, err := shell("git ls-files --exclude-standard --others")
	die(err)

	files := append(standard, others...)

	h := md5.New()
	for _, file := range files {
		if strings.TrimSpace(file) == "" {
			continue
		}
		if !versioned(file, unversioned) {
			if Verbose {
				fmt.Printf("skipping %s\n", file)
			}
			continue
		}
		if Verbose {
			fmt.Printf("hashing %s\n", file)
		}
		h.Write([]byte(file))
		info, err := os.Lstat(file)
		if err != nil {
			h.Write([]byte("error"))
			h.Write([]byte(err.Error()))
		} else {
			if info.Mode()&os.ModeSymlink != 0 {
				target, err := os.Readlink(file)
				die(err)
				h.Write([]byte("link"))
				h.Write([]byte(target))
			} else if !info.IsDir() {
				h.Write([]byte("file"))
				f, err := os.Open(file)
				die(err, file)
				_, err = io.Copy(h, f)
				f.Close()
				die(err)
			}
		}
	}

	return h.Sum(nil)
}

func shell(command string) ([]string, error) {
	cmd := exec.Command("sh", "-c", command)
	out, err := cmd.CombinedOutput()
	str := string(out)
	lines := strings.Split(str, "\n")
	return lines, err
}
