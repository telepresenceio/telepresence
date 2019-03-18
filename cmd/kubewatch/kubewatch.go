package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/datawire/teleproxy/pkg/k8s"
	"github.com/datawire/teleproxy/pkg/limiter"
	"github.com/datawire/teleproxy/pkg/tpu"
	"github.com/spf13/cobra"
)

type Syncer struct {
	Watcher     *k8s.Watcher
	SyncCommand string
	Kinds       []string
	Mux         *sync.Mutex // protects the whole data structure
	Changed     *sync.Cond
	Dirty       bool
	Limiter     limiter.Limiter
	ModTime     time.Time
	SyncCount   int
	WarmupDelay time.Duration
	router      *http.ServeMux
	port        string
	snapshotMux sync.Mutex // protects just the snapshot map
	snapshots   map[string]map[string][]byte
}

func (s *Syncer) maybeSync() {
	if s.Dirty {
		delay := s.Limiter.Limit(s.ModTime)
		if delay == 0 {
			s.Dirty = false
			s.sync()
		} else if delay > 0 {
			// if we are delaying an event we need an
			// artificial wakeup just in case there are no
			// more events to trigger syncing... if there
			// are events prior to this, then this should
			// end up being a noop because s.Dirty will be
			// false
			log.Printf("rate limiting, will sync after %s", delay.String())
			time.AfterFunc(delay, func() {
				s.Mux.Lock()
				defer s.Mux.Unlock()
				log.Printf("triggering delayed sync")
				s.ModTime = time.Now()
				s.Changed.Broadcast()
			})
		}
	}
}

func (s *Syncer) sync() {
	s.SyncCount += 1
	snapshot_id := s.write()
	s.invoke(snapshot_id)
}

func (s *Syncer) write() string {
	s.snapshotMux.Lock()
	defer s.snapshotMux.Unlock()
	snapshot_id := fmt.Sprintf("%d", s.SyncCount)
	s.snapshots[snapshot_id] = make(map[string][]byte)
	for _, kind := range s.Kinds {
		resources := s.Watcher.List(kind)
		bytes, err := k8s.MarshalResources(resources)
		if err != nil {
			panic(err)
		}
		s.snapshots[snapshot_id][kind] = bytes
		for _, rsrc := range resources {
			qname := path.Join(kind, rsrc.Namespace(), rsrc.Name())
			bytes, err := k8s.MarshalResource(rsrc)
			if err != nil {
				panic(err)
			}
			s.snapshots[snapshot_id][qname] = bytes
		}
	}
	s.cleanup()
	return snapshot_id
}

func (s *Syncer) cleanup() {
	for k := range s.snapshots {
		keep := false
		for c := s.SyncCount; c > s.SyncCount-10; c-- {
			id := fmt.Sprintf("%d", c)
			if id == k {
				keep = true
			}
		}
		if !keep {
			delete(s.snapshots, k)
			log.Printf("deleting snapshot %s", k)
		}
	}
}

func (s *Syncer) invoke(snapshot_id string) {
	k := tpu.NewKeeper("SYNC", fmt.Sprintf("%s http://localhost:%s/api/snapshot/%s", s.SyncCommand, s.port, snapshot_id))
	k.Limit = 1
	k.Start()
	k.Wait()
}

func (s *Syncer) Run() {
	go func() {
		time.Sleep(s.WarmupDelay)
		s.Mux.Lock()
		defer s.Mux.Unlock()
		for {
			s.Changed.Wait()
			s.maybeSync()
		}
	}()

	for _, k := range s.Kinds {
		// this alias is important so the func picks up the
		// value from the current iteration instead of the
		// value from the last iteration
		kind := k
		err := s.Watcher.WatchNamespace(NAMESPACE, kind, func(_ *k8s.Watcher) {
			s.Mux.Lock()
			defer s.Mux.Unlock()
			s.Dirty = true
			s.ModTime = time.Now()
			s.Changed.Broadcast()
		})
		if err != nil {
			log.Fatalf("kubewatch: %v", err)
		}
	}
	s.Watcher.Start()
	s.serve()
}

func (s *Syncer) serve() {
	s.routes()

	ln, err := net.Listen("tcp", fmt.Sprintf(":%s", PORT))
	if err != nil {
		// Error starting or closing listener:
		log.Fatalf("kubewatch: %v", err)
	}

	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		log.Fatalf("kubewatch: %v", err)
	}
	s.port = port

	server := http.Server{
		Handler: s.router,
	}

	if err := server.Serve(ln); err != http.ErrServerClosed {
		// Error starting or closing listener:
		log.Fatalf("kubewatch: %v", err)
	}
}

func (s *Syncer) routes() {
	s.router.HandleFunc("/api/snapshot/", s.safe(s.handleSnapshot()))
}

func (s *Syncer) safe(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if r := recover(); r != nil {
				var msg string
				switch e := r.(type) {
				case error:
					msg = e.Error()
				default:
					msg = fmt.Sprintf("%v", r)
				}
				http.Error(w, fmt.Sprintf("Server Error: %s", msg), http.StatusInternalServerError)
			}
		}()
		h(w, r)
	}
}

func (s *Syncer) handleSnapshot() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.snapshotMux.Lock()
		defer s.snapshotMux.Unlock()
		parts := strings.Split(r.URL.Path, "/")
		parts = parts[3:]
		snapshot_id := parts[0]
		snapshot, ok := s.snapshots[snapshot_id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		qname := strings.Join(parts[1:], "/")
		var body []byte
		if qname != "" {
			body, ok = snapshot[qname]
			if !ok {
				http.NotFound(w, r)
			}
		} else {
			var keys []string
			for k := range snapshot {
				keys = append(keys, k)
			}
			body = []byte(fmt.Sprintf("Available snapshot keys:\n - %s\n", strings.Join(keys, "\n - ")))
		}
		w.Write(body)
	}
}

var Version = "(unknown version)"

var KUBEWATCH = &cobra.Command{
	Use:  "kubewatch [options] <resources>",
	Args: cobra.MinimumNArgs(1),
	Run:  kubewatch,
}

func init() {
	KUBEWATCH.Version = Version
	KUBEWATCH.Flags().StringVarP(&PORT, "port", "p", "0", "port for kubewatch api")
	KUBEWATCH.Flags().StringVarP(&SYNC_COMMAND, "sync", "s", "curl", "sync command")
	KUBEWATCH.Flags().StringVarP(&NAMESPACE, "namespace", "n", "", "namespace to watch (defaults to all)")
	KUBEWATCH.Flags().DurationVarP(&MIN_INTERVAL, "min-interval", "m", 250*time.Millisecond, "min sync interval")
	KUBEWATCH.Flags().DurationVarP(&WARMUP_DELAY, "warmup-delay", "w", 0, "warmup delay")
}

var (
	PORT         string
	SYNC_COMMAND string
	NAMESPACE    string
	MIN_INTERVAL time.Duration
	WARMUP_DELAY time.Duration
)

func kubewatch(cmd *cobra.Command, args []string) {
	mux := &sync.Mutex{}
	cond := sync.NewCond(mux)
	s := Syncer{
		Mux:         mux,
		Changed:     cond,
		Watcher:     k8s.NewClient(nil).Watcher(),
		SyncCommand: SYNC_COMMAND,
		Kinds:       args,
		Limiter:     limiter.NewIntervalLimiter(MIN_INTERVAL),
		WarmupDelay: WARMUP_DELAY,
		router:      http.NewServeMux(),
		snapshots:   make(map[string]map[string][]byte),
	}

	s.Run()
}

func main() {
	KUBEWATCH.Execute()
}
