package testprocess

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"runtime/debug"

	"github.com/datawire/teleproxy/pkg/dtest"
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
		cmd = exec.Command(os.Args[0], "-testprocess.name", name)
		s.sudo = true
	} else {
		user := os.Getenv("SUDO_USER")
		if len(user) == 0 {
			user = os.Getenv("USER")
		}
		cmd = exec.Command("sudo", "-u", user, "-E", os.Args[0], "-testprocess.name", name)
	}
	cmd.Env = append(os.Environ(), fmt.Sprintf("TESTPROCESS_NAME=%s", name))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

var singleton = &sub{false, make(map[string]func())}

// Dispatch can be used to launch multiple subprocesses as part of a
// go test. If you want to throw up in your mouth a little, then read
// the implementation. Don't worry, this is apparently a "blessed"
// hack for doing this sort of thing.
//
// You always want to call testprocess.Dispatch() at the beginning of
// your TestMain function since confusing things will happen if you
// call it later on or have complex logic surrounding calls to it. For
// each subprocess you want, use testprocess.Make to create an
// *exec.Cmd variable and save it in a global (it must be global for
// this to work). The resulting commands that are returned can be
// started/stopped at any point later in your test, e.g.:
//
//     func TestMain(m *testing.M) {
//         testprocess.Dispatch()
//         os.Exit(m.Run())
//     }
//
//     var fooCmd = testprocess.Make(func() { doFoo(); })
//     var barCmd = testprocess.Make(func() { doBar(); })
//     var bazcmd = testprocess.Make(func() { doBaz(); })
//
//     func TestSomething(t *testing.T) {
//         ...
//         err := fooCmd.Run()
//         ...
//     }
func Dispatch() {
	// if this ever causes problems, we can switch back to getting
	// the name from the TESTPROCESS_NAME environment variable
	name := flag.String("testprocess.name", "", "")
	flag.Parse()

	if *name == "" {
		if singleton.sudo && os.Geteuid() != 0 {
			dtest.Sudo()
		}
	} else {
		log.Printf("TESTPROCESS %s PID: %d", *name, os.Getpid())

		defer func() {
			if r := recover(); r != nil {
				stack := string(debug.Stack())
				log.Printf("TESTPROCESS %s PANICKED: %v\n%s", *name, r, stack)
				os.Exit(1)
			}
		}()

		singleton.functions[*name]()

		log.Printf("TESTPROCESS %s NORMAL EXIT", *name)
		os.Exit(0)
	}
}

// Make returns an *exec.Cmd that will execute the supplied function
// in a subprocess. For this to work, testprocess.Dispatch must be
// invoked by the TestMain of any test suite using this, and the
// call to Make must be from a global variable initializer, e.g.:
//
//     var myCmd = testprocess.Make(func() { doSomething(); })
//
func Make(f func()) *exec.Cmd {
	return singleton._make(false, f)
}

// MakeSudo does the same thing as testprocess.Make with exactly the
// same limitations, except the subprocess will run as root. Note that
// if testprocess.MakeSudo is used in any part of the test suite, then
// all the normal test code will also run as root, however any
// subprocess created via testprocess.Make will run as the user.
func MakeSudo(f func()) *exec.Cmd {
	return singleton._make(true, f)
}
