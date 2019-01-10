package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/datawire/teleproxy/pkg/k8s"
	"github.com/datawire/teleproxy/pkg/tpu"
)

func envBool(name string) bool {
	val := os.Getenv(name)
	switch strings.TrimSpace(strings.ToLower(val)) {
	case "true":
		return true
	case "yes":
		return true
	case "1":
		return true
	case "false":
		return false
	case "no":
		return false
	case "0":
		return false
	case "":
		return false
	}

	return true
}

var Version = "(unknown version)"
var show_version = flag.Bool("version", false, "output version information and exit")
var debug = flag.Bool("debug", envBool("KUBEAPPLY_DEBUG"), "enable debug mode, expanded files will be preserved")
var timeout = flag.Int("t", 60, "timeout in seconds")
var files tpu.ArrayFlags

func main() {
	flag.Var(&files, "f", "path to yaml file")
	flag.Parse()

	if *show_version {
		fmt.Println("kubeapply", "version", Version)
		os.Exit(0)
	}

	if len(files) == 0 {
		fmt.Printf("at least one file argument is required")
		os.Exit(1)
	}

	p := NewPhaser()

	for _, file := range files {
		err := p.Add(file)
		if err != nil {
			log.Fatal(err)
		}
	}

	for _, names := range p.phases() {
		phase(names, nil)
	}
}

type Phaser struct {
	prefixes map[string][]string
	last     []string
}

func NewPhaser() *Phaser {
	return &Phaser{
		prefixes: make(map[string][]string),
	}
}

func isYaml(name string) bool {
	for _, ext := range []string{
		".yaml",
	} {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}

func (p *Phaser) Add(root string) error {
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() && isYaml(path) {
			p.AddFile(path)
		}

		return nil
	})
	return err
}

func (p *Phaser) AddFile(path string) {
	base := filepath.Base(path)
	var pfx string
	if len(base) >= 3 {
		pfx = base[:3]
	}
	if len(pfx) == 3 && pfx[2] == '-' && unicode.IsDigit(rune(pfx[0])) && unicode.IsDigit(rune(pfx[1])) {
		p.prefixes[pfx] = append(p.prefixes[pfx], path)
	} else {
		p.last = append(p.last, path)
	}
}

func (p *Phaser) phases() (result [][]string) {
	var keys []string
	for k := range p.prefixes {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		result = append(result, p.prefixes[k])
	}

	if len(p.last) > 0 {
		result = append(result, p.last)
	}
	return
}

func phase(names []string, data interface{}) {
	expanded, err := expand(names, data)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	waiter := k8s.NewWaiter(nil)

	valid := make(map[string]bool)
	abort := false
	for _, n := range expanded {
		err := waiter.Scan(n)
		if err != nil {
			fmt.Printf("%s: %v\n", n, err)
			valid[n] = false
			abort = true
		} else {
			valid[n] = true
		}
	}

	if !abort {
		apply(expanded)
	}

	if !*debug {
		for _, n := range expanded {
			if valid[n] {
				err := os.Remove(n)
				if err != nil {
					log.Print(err)
				}
			}
		}
	}

	if abort {
		return
	}

	if !waiter.Wait(time.Duration(*timeout) * time.Second) {
		panic("not ready")
	}
}

func expand(names []string, data interface{}) ([]string, error) {
	fmt.Printf("expanding %s\n", strings.Join(names, " "))
	var result []string
	for _, n := range names {
		resources, err := k8s.LoadResources(n)
		if err != nil {
			return nil, err
		}
		out := n + ".o"
		err = k8s.SaveResources(out, resources)
		if err != nil {
			return nil, err
		}
		result = append(result, out)
	}
	return result, nil
}

func apply(names []string) {
	args := []string{"apply"}
	for _, n := range names {
		args = append(args, "-f", n)
	}
	fmt.Printf("kubectl %s\n", strings.Join(args, " "))
	cmd := exec.Command("kubectl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err != nil {
		panic(err)
	}
}
