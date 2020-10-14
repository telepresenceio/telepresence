package docker

import (
	"bufio"
	"io"
	"log"
	"os/exec"
	"strings"
	"time"

	"github.com/datawire/ambassador/pkg/tpu"
)

type empty struct{}

type Watcher struct {
	Containers map[string]string
	stop       chan empty
	done       chan empty
}

func NewWatcher() *Watcher {
	return &Watcher{
		Containers: make(map[string]string),
		stop:       make(chan empty),
		done:       make(chan empty),
	}
}

func (w *Watcher) log(line string, args ...interface{}) {
	log.Printf("DKR: "+line, args...)
}

func (w *Watcher) Start(listener func(w *Watcher)) {
	go func() {
		wakeup := w.waiter()
		defer close(w.done)
		for {
			select {
			case <-w.stop:
				return
			case <-wakeup:
				containers, err := w.containers()
				if err == nil {
					updated := false
					for key := range w.Containers {
						if containers[key] == "" {
							delete(w.Containers, key)
							updated = true
						}
					}
					for key, value := range containers {
						prev := w.Containers[key]
						if value != prev {
							w.Containers[key] = value
							updated = true
						}
					}
					if updated {
						listener(w)
					}
				}
			}
		}
	}()
}

func (w *Watcher) Stop() {
	close(w.stop)
	<-w.done
}

func (w *Watcher) containers() (result map[string]string, err error) {
	ids, err := tpu.CmdLogf([]string{"docker", "container", "list", "-q"}, w.log)
	if err != nil {
		return
	}

	lines := ""
	if ids != "" {
		lines, err = tpu.CmdLogf(append([]string{"docker", "inspect", "--format={{.Name}} {{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", "--"}, strings.Fields(ids)...), w.log)
		if err != nil {
			return
		}
	}

	result = make(map[string]string)
	for _, line := range strings.Split(lines, "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 {
			name := strings.TrimLeft(parts[0], "/")
			ip := parts[1]
			result[name] = ip
		} else if len(parts) > 2 {
			w.log("error parsing: %s", line)
		}
	}

	return
}

func (w *Watcher) checkDocker(warn bool) bool {
	output, err := tpu.Cmd("docker", "version")
	if err != nil {
		if warn {
			w.log(output)
			w.log(err.Error())
			w.log("docker is required for docker bridge functionality")
		}
		return false
	}
	return true
}

func (w *Watcher) waiter() chan empty {
	result := make(chan empty)
	go func() {
		var pipe io.ReadCloser
		var events *bufio.Reader

		for {
			for count := 0; true; count += 1 {
				if w.checkDocker((count % 60) == 0) {
					break
				} else {
					time.Sleep(1 * time.Second)
				}
			}

			result <- empty{}
			if pipe == nil {
				pipe = w.containerEvents()
				events = bufio.NewReader(pipe)
			}

			st, err := events.ReadString('\n')
			if st != "" {
				w.log(st)
			}
			if err != nil {
				if err != io.EOF {
					log.Println(err)
				}
				pipe.Close()
				pipe = nil
				time.Sleep(1 * time.Second)
			}
		}
	}()
	return result
}

func (w *Watcher) containerEvents() io.ReadCloser {
	command := []string{"docker", "events",
		"--filter", "type=container",
		"--filter", "event=start",
		"--filter", "event=die"}
	w.log(strings.Join(command, " "))
	cmd := exec.Command(command[0], command[1:]...)
	events, err := cmd.StdoutPipe()
	if err != nil {
		w.log(err.Error())
		return nil
	}

	err = cmd.Start()
	if err != nil {
		w.log(err.Error())
		return nil
	}

	return events
}
