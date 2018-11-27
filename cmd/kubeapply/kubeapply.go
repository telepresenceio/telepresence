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

	"github.com/datawire/teleproxy/internal/pkg/tpu"

	"github.com/datawire/teleproxy/pkg/k8s/waiter"
)

var timeout = flag.Int("t", 60, "timeout in seconds")
var files tpu.ArrayFlags

func main() {
	flag.Var(&files, "f", "path to yaml file")
	flag.Parse()

	p := NewPhaser()

	for _, file := range files {
		err := p.Add(file)
		if err != nil {
			log.Fatal(err)
		}
	}

	for _, names := range p.phases() {
		phase(names)
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

	result = append(result, p.last)
	return
}

func phase(names []string) {
	args := []string{"apply"}
	for _, n := range names {
		args = append(args, "-f", n)
	}
	run("kubectl", args...)
	wait(names)
}

func run(command string, args ...string) {
	fmt.Printf("%s %s\n", command, strings.Join(args, " "))
	cmd := exec.Command(command, args...)
	out, err := cmd.CombinedOutput()
	fmt.Print(string(out))
	if err != nil {
		panic(err)
	}
}

func wait(names []string) {
	w := waiter.NewWaiter(nil)
	err := w.ScanPaths(names)
	if err != nil {
		log.Fatal(err)
	}
	if !w.Wait(time.Duration(*timeout) * time.Second) {
		panic("not ready")
	}

}
