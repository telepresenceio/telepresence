package dtest

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"runtime/debug"
)

func getFunctionName(i interface{}) string {
	return runtime.FuncForPC(reflect.ValueOf(i).Pointer()).Name()
}

type sub struct {
	sudo      bool
	functions map[string]func()
}

/* #nosec */
func (s *sub) _make(sudo bool, f func()) *exec.Cmd {
	name := getFunctionName(f)
	s.functions[name] = f
	var cmd *exec.Cmd
	if sudo {
		cmd = exec.Command(os.Args[0])
		s.sudo = true
	} else {
		cmd = exec.Command("sudo", "-u", os.Getenv("SUDO_USER"), "-E", os.Args[0])
	}
	cmd.Env = append(os.Environ(), fmt.Sprintf("BE_SUBPROCESS=%s", name))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

func (s *sub) Make(f func()) *exec.Cmd {
	return s._make(false, f)
}

func (s *sub) MakeSudo(f func()) *exec.Cmd {
	return s._make(true, f)
}

func (s *sub) Enable() {
	name := os.Getenv("BE_SUBPROCESS")
	if name == "" {
		if s.sudo && os.Geteuid() != 0 {
			Sudo()
		}
	} else {
		log.Printf("SUBPROCESS %s PID: %d", name, os.Getpid())

		defer func() {
			if r := recover(); r != nil {
				stack := string(debug.Stack())
				log.Printf("SUBPROCESS %s PANICKED: %v\n%s", name, r, stack)
				os.Exit(1)
			}
		}()

		s.functions[name]()

		log.Printf("SUBPROCESS %s NORMAL EXIT", name)
		os.Exit(0)
	}
}

// Subprocess can be used to launch multiple subprocesses as part of a
// go test. If you want to throw up in your mouth a little, then read
// the implementation. Don't worry, this is apparently a "blessed"
// hack for doing this sort of thing.
//
// You always want to call Subprocess.Enable() at the beginning of
// your TestMain function since confusing things will happen if you
// call it later on or have complex logic surrounding calls to it. For
// each subprocess you want, use Subprocess.Make to create an
// *exec.Cmd variable and save it in a global (it must be global for
// this to work). The resulting commands that are returned can be
// started/stopped at any point later in your test, e.g.:
//
//     func TestMain(m *testing.M) {
//         Subprocess.Enable()
//         os.Exit(m.Run())
//     }
//
//     var fooCmd = Subprocess.Make(func() { doFoo(); })
//     var barCmd = Subprocess.Make(func() { doBar(); })
//     var bazcmd = Subprocess.Make(func() { doBaz(); })
//
//     func TestSomething(t *testing.T) {
//         ...
//         err := fooCmd.Run()
//         ...
//     }
var Subprocess = &sub{false, make(map[string]func())}
