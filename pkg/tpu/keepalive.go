package tpu

import (
	"bufio"
	"io"
	"log"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

type Keeper struct {
	Prefix  string
	Command string
	Input   string
	Inspect string
	Limit   int
	stop    chan empty
	done    chan empty
}

func NewKeeper(prefix, command string) (k *Keeper) {
	return &Keeper{
		Prefix:  prefix,
		Command: command,
		stop:    make(chan empty),
		done:    make(chan empty),
	}
}

func (k *Keeper) Stop() {
	close(k.stop)
	k.Wait()
}

func (k *Keeper) Wait() {
	<-k.done
}

func (k *Keeper) log(line string, args ...interface{}) {
	log.Printf(k.Prefix+": "+line, args...)
}

func (k *Keeper) Start() {
	go func() {
		count := 0
		defer close(k.done)
		for {
			cmd := exec.Command("sh", "-c", k.Command)
			cmd.SysProcAttr = &syscall.SysProcAttr{
				Setpgid: true,
			}
			k.log("%s", k.Command)
			l := k.forwardOutput(cmd)

			err := writeInput(cmd, k.Input)
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
					k.log("%s", err.Error())
				}
				died <- nil
			}()

			count += 1

			select {
			case <-died:
				l.Wait()
				if count < k.Limit || k.Limit == 0 {
					k.log("%s restarting...", strings.Fields(k.Command)[0])
					ShellLog(k.Inspect, func(line string) {
						k.log("%s", line)
					})
					time.Sleep(time.Second)
				} else {
					return
				}
			case <-k.stop:
				cmd.Process.Kill()
				l.Wait()
				return
			}

		}
	}()
}

func (k *Keeper) forwardOutput(cmd *exec.Cmd) Latch {
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		panic(err)
	}
	l := NewLatch(2)
	go k.reader(pipe, l)
	pipe, err = cmd.StderrPipe()
	if err != nil {
		panic(err)
	}
	go k.reader(pipe, l)
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

func (k *Keeper) reader(pipe io.ReadCloser, l Latch) {
	defer pipe.Close()
	buf := bufio.NewReader(pipe)
	for {
		line, err := buf.ReadString('\n')
		if err != nil {
			if strings.TrimSpace(line) != "" {
				k.log("%s", line)
			}
			if err != io.EOF {
				k.log("%s", err.Error())
			}
			l.Notify()
			return
		} else {
			k.log("%s", line)
		}
	}
}
