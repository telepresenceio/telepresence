package usg

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	usgrpc "github.com/telepresenceio/telepresence/rpc/v2/usg"
)

// DiskSink is the on-disk FIFO used by the client. Each pending report is a
// separate file holding the binary proto encoding of UsageReport. Filenames
// are timestamp-prefixed so directory ordering matches insertion order, which
// gives natural FIFO semantics for multiple concurrent writers (the CLI, the
// user daemon, and the root daemon all share the same directory).
//
// The directory is capped at MaxQueueSize entries. On overflow the oldest
// files are removed so the most recent reports are always retained.
type DiskSink struct {
	dir string
	cap int
	// mu serializes Enqueue/Drain within a single process. Cross-process
	// safety relies on each report having a unique filename (timestamp + a
	// monotonic counter + the process pid), so concurrent writers never
	// collide.
	mu  sync.Mutex
	seq atomic.Uint64
}

// NewDiskSink prepares dir as the FIFO directory. dir is created if it does
// not exist. Returns an error only if the directory cannot be created; missing
// or unreadable files at drain time are handled silently.
func NewDiskSink(dir string) (*DiskSink, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &DiskSink{dir: dir, cap: MaxQueueSize}, nil
}

func (d *DiskSink) Enqueue(r *usgrpc.UsageReport) {
	if r == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	data, err := proto.Marshal(r)
	if err != nil {
		return
	}

	// Evict the oldest files first, so the directory after this insert holds
	// at most d.cap entries. Sorting by name matches insertion order because
	// nextName begins with a UTC timestamp.
	entries, _ := os.ReadDir(d.dir)
	pbs := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".pb" {
			pbs = append(pbs, e.Name())
		}
	}
	sort.Strings(pbs)
	for len(pbs) >= d.cap {
		_ = os.Remove(filepath.Join(d.dir, pbs[0]))
		pbs = pbs[1:]
	}

	name := d.nextName()
	tmp := filepath.Join(d.dir, name+".tmp")
	final := filepath.Join(d.dir, name+".pb")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	if err := os.Rename(tmp, final); err != nil {
		// Don't leave the half-written tmp file behind; eviction only
		// counts .pb entries so a stale .tmp would never be cleaned up.
		_ = os.Remove(tmp)
	}
}

func (d *DiskSink) Drain(limit int) []*usgrpc.UsageReport {
	d.mu.Lock()
	defer d.mu.Unlock()

	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".pb" {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if limit <= 0 || limit > len(names) {
		limit = len(names)
	}
	out := make([]*usgrpc.UsageReport, 0, limit)
	for _, n := range names[:limit] {
		path := filepath.Join(d.dir, n)
		data, err := os.ReadFile(path)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				_ = os.Remove(path)
			}
			continue
		}
		r := new(usgrpc.UsageReport)
		if err := proto.Unmarshal(data, r); err != nil {
			_ = os.Remove(path)
			continue
		}
		_ = os.Remove(path)
		out = append(out, r)
	}
	return out
}

func (d *DiskSink) Len() int {
	d.mu.Lock()
	entries, _ := os.ReadDir(d.dir)
	n := 0
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".pb" {
			n++
		}
	}
	d.mu.Unlock()
	return n
}

func (d *DiskSink) nextName() string {
	seq := d.seq.Add(1)
	// Format: YYYYMMDDTHHMMSS.NNNNNNNNN-SSSS-PPPP where the second-tier suffix
	// is a per-instance monotonic counter and the third tier is the pid; the
	// combination is unique across concurrent CLI processes that share the
	// same cache directory.
	now := time.Now().UTC()
	return now.Format("20060102T150405.000000000") +
		"-" + uintHex(seq) +
		"-" + uintHex(uint64(os.Getpid()))
}

func uintHex(v uint64) string {
	const hex = "0123456789abcdef"
	if v == 0 {
		return "0"
	}
	var buf [16]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = hex[v&0xf]
		v >>= 4
	}
	return string(buf[i:])
}
