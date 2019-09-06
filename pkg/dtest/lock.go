package dtest

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

const pattern = "/tmp/datawire-machine-scoped-%s.lock"

func exit(filename string, err error) {
	fmt.Fprintf(os.Stderr, "error trying to acquire lock on %s: %v\n", filename, err)
	os.Exit(1)
}

// WithMachineLock executes the supplied body with a guarantee that it
// is the only code running (via WithMachineLock) on the machine.
func WithMachineLock(body func()) {
	WithNamedMachineLock("default", body)
}

// WithNamedMachineLock executes the supplied body with a guarantee
// that it is the only code running (via WithMachineLock) on the
// machine. The name provides scope so this can be used in multiple
// independent ways without conflicts.
func WithNamedMachineLock(name string, body func()) {
	lockAcquireStart := time.Now()
	filename := fmt.Sprintf(pattern, name)
	var file *os.File
	var err error
	func() {
		old := syscall.Umask(0)
		defer syscall.Umask(old)
		/* #nosec */
		file, err = os.OpenFile(filename, os.O_RDONLY|os.O_CREATE, 0666)
		if err != nil && os.IsPermission(err) {
			// On Linux, if
			//   1. the file already exists, and
			//   2. the file is owned by a different user (e.g., the normal user, and we're root), and
			//   3. the file is in a directory with the sticky bit set,
			// then O_CREATE will cause "permission denied" (observed on Linux 5.2.1).
			// Flynn and Luke both agree that this behavior is brain-dead enough that we
			// think it's a bug in Linux. O_CREATE semantics should not come in to play
			// if the file already exists.
			var _err error
			file, _err = os.OpenFile(filename, os.O_RDONLY, 0666)
			if _err == nil {
				err = _err
			}
		}
	}()

	if err != nil {
		exit(filename, err)
	}

	err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
	if err != nil {
		err = &os.PathError{Op: "flock", Path: file.Name(), Err: err}
		exit(filename, err)
	}
	defer func() {
		file.Close()
	}()

	fmt.Printf("Acquiring machine lock %q took %.2f seconds\n", name, time.Since(lockAcquireStart).Seconds())
	body()
}
