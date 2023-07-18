package logging

import (
	"fmt"
	"os"
	"syscall" //nolint:depguard // We specifically need "syscall.Stat_t" rather than "unix.Stat_t" for fs.File.Sys().
	"time"

	"github.com/telepresenceio/telepresence/v2/pkg/dos"
)

type fileInfo struct {
	*syscall.Stat_t
}

func osFStat(file dos.File) (SysInfo, error) {
	stat, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to stat %s: %w", file.Name(), err)
	}
	sys, ok := stat.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, fmt.Errorf("files of type %T don't support Fstat", file)
	}
	return fileInfo{sys}, nil
}

func (u fileInfo) Size() int64 {
	return u.Stat_t.Size
}

func (u fileInfo) SetOwnerAndGroup(name string) error {
	return os.Chown(name, int(u.Uid), int(u.Gid))
}

func (u fileInfo) HaveSameOwnerAndGroup(other SysInfo) bool {
	ou := other.(fileInfo)
	return u.Uid == ou.Uid && u.Gid == ou.Gid
}

func (u fileInfo) String() string {
	return fmt.Sprintf("CTIME %v, UID %d, GID %d", u.BirthTime(), u.Uid, u.Gid)
}

func (u fileInfo) BirthTime() time.Time  { return time.Unix(u.Birthtimespec.Unix()) }
func (u fileInfo) ModifyTime() time.Time { return time.Unix(u.Mtimespec.Unix()) }
func (u fileInfo) ChangeTime() time.Time { return time.Unix(u.Ctimespec.Unix()) }
