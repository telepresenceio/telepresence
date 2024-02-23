package logging

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"

	"github.com/telepresenceio/telepresence/v2/pkg/dos"
)

type statable interface {
	Fd() uintptr
	Name() string
}

type fileInfo struct {
	size  int64
	uid   int
	gid   int
	btime time.Time
	mtime time.Time
	ctime time.Time
}

func osFStat(dfile dos.File) (SysInfo, error) {
	file, ok := dfile.(statable)
	if !ok {
		return nil, fmt.Errorf("files of type %T don't support Fstat", dfile)
	}
	const want = 0 |
		unix.STATX_SIZE |
		unix.STATX_UID |
		unix.STATX_GID |
		unix.STATX_BTIME |
		unix.STATX_MTIME |
		unix.STATX_CTIME

	var stat unix.Statx_t
	if err := unix.Statx(int(file.Fd()), "", unix.AT_EMPTY_PATH, want, &stat); err != nil {
		if errors.Is(err, unix.ENOSYS) {
			// The statx(2) system call was introduced in Linux 4.11 (2017).  That's new
			// enough that we should have a fallback.
			return oldFStat(file)
		}
		return nil, fmt.Errorf("failed to statx %s: %w", file.Name(), err)
	}

	if stat.Mask&want != want {
		// Not all filesystems (notably: tmpfs) support btime.
		return oldFStat(file)
	}

	return fileInfo{
		size:  int64(stat.Size),
		uid:   int(stat.Uid),
		gid:   int(stat.Gid),
		btime: time.Unix(stat.Btime.Sec, int64(stat.Btime.Nsec)),
		mtime: time.Unix(stat.Mtime.Sec, int64(stat.Mtime.Nsec)),
		ctime: time.Unix(stat.Ctime.Sec, int64(stat.Ctime.Nsec)),
	}, nil
}

func oldFStat(file statable) (SysInfo, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return nil, fmt.Errorf("failed to stat %s: %w", file.Name(), err)
	}
	return fileInfo{
		size: stat.Size,
		uid:  int(stat.Uid),
		gid:  int(stat.Gid),
		// The reason we wanted statx(2) in the first place is
		// because fstat(2) doesn't give us the birthtime.  Fake it
		// with the changetime.  I'm not sure why changetime is the
		// best choice, but it's what Telepresence did before we
		// added statx support.
		btime: time.Unix(stat.Ctim.Sec, stat.Ctim.Nsec),
		mtime: time.Unix(stat.Mtim.Sec, stat.Mtim.Nsec),
		ctime: time.Unix(stat.Ctim.Sec, stat.Ctim.Nsec),
	}, nil
}

func (u fileInfo) Size() int64 {
	return u.size
}

func (u fileInfo) SetOwnerAndGroup(name string) error {
	return os.Chown(name, u.uid, u.gid)
}

func (u fileInfo) HaveSameOwnerAndGroup(other SysInfo) bool {
	ou := other.(fileInfo)
	return u.uid == ou.uid && u.gid == ou.gid
}

func (u fileInfo) String() string {
	return fmt.Sprintf("BTIME %v, MTIME %v, CTIME %v, UID %d, GID %d",
		u.btime, u.mtime, u.ctime, u.uid, u.gid)
}

func (u fileInfo) BirthTime() time.Time  { return u.btime }
func (u fileInfo) ModifyTime() time.Time { return u.mtime }
func (u fileInfo) ChangeTime() time.Time { return u.ctime }
