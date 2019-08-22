package testprocess

import (
	"flag"
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

func alreadySudoed() bool {
	return os.Getuid() == 0 && os.Getenv("SUDO_USER") != ""
}

var functions = map[string]func(){}

/* #nosec */
func _make(sudo bool, f func()) *exec.Cmd {
	name := getFunctionName(f)
	functions[name] = f

	args := []string{os.Args[0], "-testprocess.name=" + name}
	switch {
	case sudo && !alreadySudoed():
		args = append([]string{"sudo", "-E", "--"}, args...)
	case !sudo && alreadySudoed():
		// In case they called dtest.Sudo() to run "everything" as root.
		args = append([]string{"sudo", "-E", "-u", os.Getenv("SUDO_USER"), "--"}, args...)
	}

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd
}

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
	name := flag.String("testprocess.name", "", "internal use")
	flag.Parse()

	if *name == "" {
		return
	}

	log.Printf("TESTPROCESS %s PID: %d", *name, os.Getpid())

	defer func() {
		if r := recover(); r != nil {
			stack := string(debug.Stack())
			log.Printf("TESTPROCESS %s PANICKED: %v\n%s", *name, r, stack)
			os.Exit(1)
		}
	}()

	functions[*name]()

	log.Printf("TESTPROCESS %s NORMAL EXIT", *name)
	os.Exit(0)
}

// Make returns an *exec.Cmd that will execute the supplied function
// in a subprocess. For this to work, testprocess.Dispatch must be
// invoked by the TestMain of any test suite using this, and the
// call to Make must be from a global variable initializer, e.g.:
//
//     var myCmd = testprocess.Make(func() { doSomething(); })
//
func Make(f func()) *exec.Cmd {
	return _make(false, f)
}

// MakeSudo does the same thing as testprocess.Make with exactly the
// same limitations, except the subprocess will run as root.
func MakeSudo(f func()) *exec.Cmd {
	return _make(true, f)
}
