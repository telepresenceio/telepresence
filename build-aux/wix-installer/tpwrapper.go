//go:build windows
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/debug"
)

const svcName = "TelepresenceDaemon"

type wrapper struct {
	executable string
	cacheDir   string
	configPath string
	logPath    string
	logLevel   string
	address    string
	pprofPort  uint
}

func (w *wrapper) Execute(_ []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	const cmdsAccepted = svc.AcceptStop | svc.AcceptShutdown

	changes <- svc.Status{State: svc.StartPending}

	args := make([]string, 0, 5)
	args = append(args, "rootd", "--config", w.configPath)
	if w.cacheDir != "" {
		args = append(args, "--cache", w.cacheDir)
	}
	if w.logPath != "" {
		args = append(args, "--logfile", w.logPath)
	}
	if w.address != "" {
		args = append(args, "--address", w.address)
	}
	if w.pprofPort > 0 {
		args = append(args, "--pprof", strconv.Itoa(int(w.pprofPort)))
	}
	args = append(args, "--managed")
	cmd := exec.Command(w.executable, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		log.Printf("Failed to start %s: %v", w.executable, err)
		changes <- svc.Status{State: svc.Stopped}
		return false, 1
	}

	// Success — we are running
	log.Println("telepresence.exe started (PID", cmd.Process.Pid, ")")
	changes <- svc.Status{State: svc.Running, Accepts: cmdsAccepted}

	// Wait for stop request or child exit
	go func() {
		_ = cmd.Wait() // ignore error, we just want to know when it dies
		changes <- svc.Status{State: svc.Stopped}
	}()

	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				changes <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				log.Println("Stopping telepresence.exe...")
				changes <- svc.Status{State: svc.StopPending}
				// Graceful SIGTERM first
				if err := cmd.Process.Kill(); err != nil {
					log.Printf("Kill failed: %v", err)
				}
				return false, 0
			}
		}
	}
}

func main() {
	var logLevel string
	isDebug := false
	w := &wrapper{}
	flag.BoolVar(&isDebug, "debug", false, "run in console, not as a real service")
	flag.StringVar(&w.executable, "executable", "", `path to the executable (required)`)
	flag.StringVar(&w.configPath, "config", "", `Path to the Telepresence configuration file (required)`)
	flag.StringVar(&logLevel, "loglevel", "info", `one of error, warning, info, debug, or trace`)
	flag.StringVar(&w.cacheDir, "cache", "", `Path to the Telepresence cache directory`)
	flag.StringVar(&w.logPath, "logfile", "", "path to the log file")
	flag.StringVar(&w.address, "address", ":4037", "TCP address to listen to")
	flag.UintVar(&w.pprofPort, "pprof", uint(0), "start pprof server on the given port")
	flag.Parse()
	if w.configPath == "" {
		flag.Usage()
		os.Exit(1)
	}
	var err error
	switch logLevel {
	case "error", "warning", "info", "debug", "trace":
		err = os.WriteFile(w.configPath, []byte(fmt.Sprintf("logLevels:\n  rootDaemon: %s\n", logLevel)), 0o644)
	default:
		err = fmt.Errorf("invalid loglevel: %q", logLevel)
	}
	if err != nil {
		log.Fatal(err)
	}
	if isDebug {
		err = debug.Run(svcName, w)
	} else {
		err = svc.Run(svcName, w)
	}
	if err != nil {
		log.Fatal(err)
	}
}
