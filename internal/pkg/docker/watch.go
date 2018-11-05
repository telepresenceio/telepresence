package docker

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"reflect"
	"strings"
	"os/exec"

	"github.com/datawire/teleproxy/internal/pkg/tpu"
)

func Watch(listener func(map[string]string)) {
	go func() {
		var prev map[string]string
		var events *bufio.Reader

		for {
			current, err := containers()
			if !reflect.DeepEqual(current, prev) {
				listener(current)
				prev = current
			}

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
			if err == io.EOF {
				events = nil
			} else if err != nil {
				log.Println(err)
			}
		}
	}()
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
		} else if len(parts) != 0 {
			err = fmt.Errorf("error parsing: %v", line)
			return
		}
	}

	return
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
