package logging

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/datawire/dlib/dtime"
)

// A RotationStrategy answers the question if it is time to rotate the file now. It is called prior to every write
// so it needs to be fairly quick.
type RotationStrategy interface {
	RotateNow(file *RotatingFile, writeSize int) bool
}

type rotateNever int

// The RotateNever strategy will always answer false to the RotateNow question
const RotateNever = rotateNever(0)

func (rotateNever) RotateNow(_ *RotatingFile, _ int) bool {
	return false
}

// A rotateOnce ensures that the file is rotated exactly once if it is of non zero size when the
// first call to Write() arrives.
type rotateOnce struct {
	called bool
}

func NewRotateOnce() RotationStrategy {
	return &rotateOnce{}
}

func (r *rotateOnce) RotateNow(rf *RotatingFile, _ int) bool {
	if r.called {
		return false
	}
	r.called = true
	return rf.Size() > 0
}

type rotateDaily int

// The RotateDaily strategy will ensure that the file is rotated if it is of non zero size when a call
// to Write() arrives on a day different from the day when the current file was created.
const RotateDaily = rotateDaily(0)

func (rotateDaily) RotateNow(rf *RotatingFile, _ int) bool {
	if rf.Size() == 0 {
		return false
	}
	bt := rf.BirthTime()
	return dtime.Now().In(bt.Location()).Day() != rf.BirthTime().Day()
}

type RotatingFile struct {
	fileMode    os.FileMode
	dirName     string
	fileName    string
	timeFormat  string
	localTime   bool
	captureStd  bool
	maxFiles    uint16
	strategy    RotationStrategy
	mutex       sync.Mutex
	removeMutex sync.Mutex

	// file is the current file. It is never nil
	file *os.File

	// size is the number of bytes written to the current file.
	size int64

	// birthTime is the time when the current file was first created
	birthTime time.Time
}

// OpenRotatingFile opens a file with the given name after first having created the directory that it
// resides in and all parent directories. The file is opened write only.
//
// Parameters:
//
// - dirName:   full path to the directory of the log file and its backups
//
// - fileName:   name of the file that should opened (relative to dirName)
//
// - timeFormat: the format to use for the timestamp that is added to rotated files
//
// - localTime: if true, use local time in timestamps, if false, use UTC
//
// - captureStd: if true, override os.Stdout and os.Stderr with the file descriptor of the current logfile
//
// - stdLogger: if not nil, all writes to os.Stdout and os.Stderr will be redirected to this logger as INFO level
// messages prefixed with <stdout> or <stderr>
//
// - fileMode: the mode to use when creating new files the file
//
// - strategy:  determines when a rotation should take place
//
// - maxFiles: maximum number of files in rotation, including the currently active logfile. A value of zero means
// unlimited
func OpenRotatingFile(
	logfilePath string,
	timeFormat string,
	localTime bool,
	captureStd bool,
	fileMode os.FileMode,
	strategy RotationStrategy,
	maxFiles uint16,
) (*RotatingFile, error) {
	logfileDir, logfileBase := filepath.Split(logfilePath)

	var err error
	if err = os.MkdirAll(logfileDir, 0755); err != nil {
		return nil, err
	}

	rf := &RotatingFile{
		dirName:    logfileDir,
		fileName:   logfileBase,
		fileMode:   fileMode,
		strategy:   strategy,
		localTime:  localTime,
		captureStd: captureStd,
		timeFormat: timeFormat,
		maxFiles:   maxFiles,
	}

	var info os.FileInfo
	if info, err = os.Stat(logfilePath); err != nil {
		if os.IsNotExist(err) {
			if err = rf.openNew(nil); err == nil {
				return rf, nil
			}
		}
		return nil, err
	}

	rf.birthTime = GetSysInfo(logfileDir, info).Birthtime()
	rf.size = info.Size()

	// Open existing file for append
	if rf.file, err = openForAppend(logfilePath, rf.fileMode); err != nil {
		return nil, err
	}
	rf.afterOpen()
	return rf, nil
}

// BirthTime returns the time when the current file was created. The time will be local if
// the file was opened with localTime == true and UTC otherwise.
func (rf *RotatingFile) BirthTime() time.Time {
	rf.mutex.Lock()
	bt := rf.birthTime
	rf.mutex.Unlock()
	return bt
}

// Close implements io.Closer
func (rf *RotatingFile) Close() error {
	return rf.file.Close()
}

// Rotate closes the currently opened file and renames it by adding a timestamp between the file name
// and its extension. A new file empty file is then opened to receive subsequent data.
func (rf *RotatingFile) Rotate() (err error) {
	rf.mutex.Lock()
	defer rf.mutex.Unlock()
	return rf.rotate()
}

// Size returns the size of the current file
func (rf *RotatingFile) Size() int64 {
	rf.mutex.Lock()
	sz := rf.size
	rf.mutex.Unlock()
	return sz
}

// Write implements io.Writer
func (rf *RotatingFile) Write(data []byte) (int, error) {
	rotateNow := rf.strategy.RotateNow(rf, len(data))
	rf.mutex.Lock()
	defer rf.mutex.Unlock()

	if rotateNow {
		if err := rf.rotate(); err != nil {
			return 0, err
		}
	}
	l, err := rf.file.Write(data)
	if err != nil {
		return 0, err
	}
	rf.size += int64(l)
	return l, nil
}

func (rf *RotatingFile) afterOpen() {
	if rf.captureStd {
		err := dupToStd(rf.file)
		if err != nil {
			// Dup2 failed (or isn't implemented on the current platform)
			os.Stdout = rf.file
			os.Stderr = rf.file
		} else {
			if os.Stdout.Fd() != 1 {
				os.Stdout = rf.file
			}
			if os.Stderr.Fd() != 2 {
				os.Stderr = rf.file
			}
		}
	}
	go rf.removeOldFiles()
}

func (rf *RotatingFile) fileTime(t time.Time) time.Time {
	if rf.localTime {
		t = t.Local()
	} else {
		t = t.UTC()
	}
	return t
}

func (rf *RotatingFile) openNew(prevInfo SysInfo) (err error) {
	fullPath := filepath.Join(rf.dirName, rf.fileName)
	var newFile *os.File
	if rf.file == nil {
		if newFile, err = createFile(fullPath, rf.fileMode); err != nil {
			return err
		}
	} else {
		// Open file with a different name so that a tail -F on the original doesn't fail with a permission denied
		tmp := fullPath + ".tmp"
		var tmpFile *os.File
		if tmpFile, err = createFile(tmp, rf.fileMode); err != nil {
			return err
		}

		stat, err := tmpFile.Stat()
		_ = tmpFile.Close()
		if err != nil {
			return err
		}

		if prevInfo != nil && !prevInfo.HaveSameOwnerAndGroup(GetSysInfo(rf.dirName, stat)) {
			if err = prevInfo.SetOwnerAndGroup(tmp); err != nil {
				_ = os.Remove(tmp)
				return err
			}
		}

		if err = os.Rename(tmp, fullPath); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		if newFile, err = openForAppend(fullPath, rf.fileMode); err != nil {
			_ = os.Remove(fullPath)
			return err
		}
	}

	oldFile := rf.file
	rf.file = newFile
	if oldFile != nil {
		if err = oldFile.Close(); err != nil {
			return err
		}
	}

	rf.birthTime = rf.fileTime(dtime.Now())
	rf.size = 0
	rf.afterOpen()
	return nil
}

// removeOldFiles checks how many files that currently exists (backups + current log file) with the same
// name as this RotatingFile and then, as long as the number of files exceed the maxFiles given to  the
// constructor, it will continuously remove the oldest file.
//
// This function should typically run in it's own goroutine
func (rf *RotatingFile) removeOldFiles() {
	rf.removeMutex.Lock()
	defer rf.removeMutex.Unlock()

	files, err := ioutil.ReadDir(rf.dirName)
	if err != nil {
		return
	}
	ext := filepath.Ext(rf.fileName)
	pfx := rf.fileName[:len(rf.fileName)-len(ext)] + "-"

	// Use a map with unix nanosecond timestamp as key
	names := make(map[int64]string, rf.maxFiles+2)

	// Slice of timestamps later to be ordered
	keys := make([]int64, 0, rf.maxFiles+2)

	for _, file := range files {
		fn := file.Name()

		// Skip files that doesn't start with the prefix and end with the suffix.
		if !(strings.HasPrefix(fn, pfx) && strings.HasSuffix(fn, ext)) {
			continue
		}
		// Parse the timestamp from the file name
		var ts time.Time
		if ts, err = time.Parse(rf.timeFormat, fn[len(pfx):len(fn)-len(ext)]); err != nil {
			continue
		}
		key := ts.UnixNano()
		keys = append(keys, key)
		names[key] = fn
	}
	mx := int(rf.maxFiles) - 1 // -1 to account for the current log file
	if len(keys) <= mx {
		return
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, key := range keys[:len(keys)-mx] {
		_ = os.Remove(filepath.Join(rf.dirName, names[key]))
	}
}

func (rf *RotatingFile) rotate() (err error) {
	var prevInfo SysInfo
	if rf.maxFiles == 0 || rf.maxFiles > 1 {
		prevStat, err := rf.file.Stat()
		if err != nil {
			return err
		}
		prevInfo = GetSysInfo(filepath.Dir(rf.dirName), prevStat)

		fullPath := filepath.Join(rf.dirName, rf.fileName)
		ex := filepath.Ext(rf.fileName)
		sf := fullPath[:len(fullPath)-len(ex)]
		ts := rf.fileTime(dtime.Now()).Format(rf.timeFormat)
		if err = os.Rename(fullPath, fmt.Sprintf("%s-%s%s", sf, ts, ex)); err != nil {
			return err
		}
	}
	return rf.openNew(prevInfo)
}
