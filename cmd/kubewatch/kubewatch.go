package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/datawire/teleproxy/pkg/k8s"
	"github.com/datawire/teleproxy/pkg/tpu"
	"github.com/spf13/cobra"
)

type Syncer struct {
	Watcher     *k8s.Watcher
	Root        string
	SyncCommand string
	Kinds       []string
	Mux         sync.Mutex
	Dirty       bool
	ModTime     time.Time
	SyncTime    time.Time
	SyncCount   int
	MinInterval time.Duration
	MaxInterval time.Duration
	WarmupDelay time.Duration
}

func (s *Syncer) maybeSync() {
	s.Mux.Lock()
	defer s.Mux.Unlock()

	if !s.Dirty {
		return
	}

	now := time.Now()
	if now.Sub(s.ModTime) > s.MinInterval || now.Sub(s.SyncTime) > s.MaxInterval {
		s.SyncTime = now
		s.Dirty = false
		s.sync()
	}
}

func (s *Syncer) sync() {
	s.SyncCount += 1
	path := s.write()
	s.cleanup()
	s.invoke(path)
}

func (s *Syncer) write() string {
	root := filepath.Join(s.Root, fmt.Sprintf("sync-%d", s.SyncCount))
	err := os.RemoveAll(root)
	if err != nil {
		log.Printf("error removing %s: %v", root, err)
	}
	for _, kind := range s.Kinds {
		for _, rsrc := range s.Watcher.List(kind) {
			s.writeResource(root, kind, rsrc)
		}
	}
	return root
}

func (s *Syncer) cleanup() {
	dirs, err := filepath.Glob(filepath.Join(s.Root, "sync-*"))
	if err != nil {
		log.Printf("error listing sync directories: %v", err)
	}
	for _, d := range dirs {
		keep := false
		for c := s.SyncCount; c > s.SyncCount-10; c-- {
			if strings.HasSuffix(d, fmt.Sprintf("sync-%d", c)) {
				keep = true
			}
		}
		if !keep {
			err = os.RemoveAll(d)
			if err != nil {
				log.Printf("error removing %s: %v", d, err)
			}
		}
	}
}

func (s *Syncer) invoke(dir string) {
	k := tpu.NewKeeper("SYNC", fmt.Sprintf("%s %s", s.SyncCommand, dir))
	k.Limit = 1
	k.Start()
	k.Wait()
}

func (s *Syncer) Run() {
	go func() {
		time.Sleep(s.WarmupDelay)
		for {
			s.maybeSync()
			time.Sleep(s.MinInterval)
		}
	}()

	for _, k := range s.Kinds {
		// this alias is important so the func picks up the
		// value from the current iteration instead of the
		// value from the last iteration
		kind := k
		s.Watcher.Watch(kind, func(_ *k8s.Watcher) {
			s.Mux.Lock()
			defer s.Mux.Unlock()
			s.Dirty = true
			s.ModTime = time.Now()
		})
	}
	s.Watcher.Start()
	s.Watcher.Wait()
}

func (s *Syncer) writeResource(root, kind string, r k8s.Resource) {
	dir := filepath.Join(root, r.Namespace(), kind)
	path := filepath.Join(dir, r.Name()+".yaml")
	log.Printf("writing %s/%s to %s", kind, r.QName(), path)
	err := os.MkdirAll(dir, 0755)
	if err != nil {
		log.Println(err)
		return
	}
	bytes, err := k8s.MarshalResource(r)
	if err != nil {
		log.Println(err)
		return
	}
	err = ioutil.WriteFile(path, bytes, 0644)
	if err != nil {
		log.Println(err)
		return
	}
}

var KUBEWATCH = &cobra.Command{
	Use:  "kubewatch [options] <resources>",
	Args: cobra.MinimumNArgs(1),
	Run:  kubewatch,
}

func init() {
	KUBEWATCH.Flags().StringVarP(&ROOT, "root", "r", "/tmp/kubewatch", "root directory for resource files")
	KUBEWATCH.Flags().StringVarP(&SYNC_COMMAND, "sync", "s", "ls -R", "sync command")
	KUBEWATCH.Flags().DurationVarP(&MIN_INTERVAL, "min-interval", "m", 250*time.Millisecond, "min sync interval")
	KUBEWATCH.Flags().DurationVarP(&MAX_INTERVAL, "max-interval", "M", time.Second, "max sync interval")
	KUBEWATCH.Flags().DurationVarP(&WARMUP_DELAY, "warmup-delay", "w", 0, "warmup delay")
}

var (
	ROOT         string
	SYNC_COMMAND string
	MIN_INTERVAL time.Duration
	MAX_INTERVAL time.Duration
	WARMUP_DELAY time.Duration
)

func kubewatch(cmd *cobra.Command, args []string) {
	s := Syncer{
		Watcher:     k8s.NewClient(nil).Watcher(),
		Root:        ROOT,
		SyncCommand: SYNC_COMMAND,
		Kinds:       args,
		MinInterval: MIN_INTERVAL,
		MaxInterval: MAX_INTERVAL,
		WarmupDelay: WARMUP_DELAY,
	}

	s.Run()
}

func main() {
	KUBEWATCH.Execute()
}
