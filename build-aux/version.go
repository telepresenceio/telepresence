// +build ignore

package main

import (
	"crypto/md5"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

var Verbose bool

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
	fmt.Printf("%x\n", hash())
}

func hash() []byte {
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
