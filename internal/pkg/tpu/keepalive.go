package tpu

import (
	"io"
	"log"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

type keeper struct {
	shutdown chan empty
	done     chan empty
}

func (k keeper) Shutdown() {
	k.shutdown <- nil
	k.Wait()
}

func (k keeper) Wait() {
	<-k.done
}

func Keepalive(limit int, input string, program string, args ...string) (k keeper) {
	k = keeper{
		shutdown: make(chan empty),
		done:     make(chan empty),
	}
	go func() {
		count := 0
	OUTER:
		for {
			cmd := exec.Command(program, args...)
			cmd.SysProcAttr = &syscall.SysProcAttr{
				Setpgid: true,
			}
			l := forwardOutput(cmd)

			err := writeInput(cmd, input)
			if err != nil {
				panic(err)
			}

			err = cmd.Start()
			if err != nil {
				panic(err)
			}

			died := make(chan empty)
			go func() {
				err = cmd.Wait()
				if err != nil {
					log.Println(program, err)
				}
				died <- nil
			}()

			count += 1

			select {
			case <-died:
				l.Wait()
				if count < limit || limit == 0 {
					log.Println(program, "restarting...")
					time.Sleep(time.Second)
				} else {
					break OUTER
				}
			case <-k.shutdown:
				cmd.Process.Kill()
				l.Wait()
				break OUTER
			}

		}
		k.done <- nil
	}()
	return
}

func forwardOutput(cmd *exec.Cmd) Latch {
	log.Println(strings.Join(cmd.Args, " "))
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		panic(err)
	}
	l := NewLatch(2)
	go reader(pipe, l)
	pipe, err = cmd.StderrPipe()
	if err != nil {
		panic(err)
	}
	go reader(pipe, l)
	return l
}

func writeInput(cmd *exec.Cmd, input string) error {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	_, err = stdin.Write([]byte(input))
	if err != nil {
		return err
	}
	err = stdin.Close()
	return err
}

func reader(pipe io.ReadCloser, l Latch) {
	const size = 64 * 1024
	var buf [size]byte
	for {
		n, err := pipe.Read(buf[:size])
		if err != nil {
			pipe.Close()
			l.Notify()
			return
		}
		log.Printf("%s", buf[:n])
	}
}
