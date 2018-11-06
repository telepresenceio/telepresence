package docker

import (
	"bufio"
	"io"
	"log"
	"strings"
	"os/exec"

	"github.com/datawire/teleproxy/internal/pkg/tpu"
)

type empty struct{}

type Watcher struct {
	Containers map[string]string
	stop chan empty
	done chan empty
}

func NewWatcher() *Watcher {
	return &Watcher{
		Containers: make(map[string]string),
		stop: make(chan empty),
		done: make(chan empty),
	}
}

func (w *Watcher) Start(listener func(w *Watcher)) {
	go func() {
		wakeup := waiter()
		OUTER: for {
			select {
			case <- w.stop:
				break OUTER
			case <- wakeup:
				containers, err := containers()
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
		close(w.done)
	}()
}

func (w *Watcher) Stop() {
	close(w.stop)
	<- w.done
}

func containers() (result map[string]string, err error) {
	lines, err := tpu.Shell("docker inspect -f '{{.Name}} {{.NetworkSettings.IPAddress}}'  $(docker ps -q)")
	if err != nil { return }

	result = make(map[string]string)
	for _, line := range strings.Split(lines, "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 {
			name := strings.TrimLeft(parts[0], "/")
			ip := parts[1]
			result[name] = ip
		} else if len(parts) > 2 {
			log.Printf("error parsing: %v", line)
		}
	}

	return
}

func waiter() chan empty {
	result := make(chan empty)
	go func() {
		var events *bufio.Reader

		for {
			result <- empty{}
			if events == nil {
				events = containerEvents()
			}

			st, err := events.ReadString('\n')
			if st != "" {
				if st[len(st)-1] != '\n' {
					log.Println(st)
				} else {
					log.Print(st)
				}
			}
			if err != nil {
				if err != io.EOF {
					log.Println(err)
				}
				events = nil
			}
		}
	}()
	return result
}

func containerEvents() *bufio.Reader {
	command := "docker events --filter 'type=container' --filter 'event=start' --filter 'event=die'"
	log.Println(command)
	cmd := exec.Command("sh", "-c", command)
	ubevents, err := cmd.StdoutPipe()
	if err != nil {
		log.Println(err)
		return nil
	}

	err = cmd.Start()
	if err != nil {
		log.Println(err)
		return nil
	}

	return bufio.NewReader(ubevents)
}
