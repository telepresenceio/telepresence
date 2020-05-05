package edgectl

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/pkg/errors"
)

// ClientMessage contains everything the daemon needs to process a
// user's command
type ClientMessage struct {
	Args          []string
	RAI           *RunAsInfo
	APIVersion    int
	ClientVersion string
	InstallID     string
}

// ExitPrefix is the token used by the daemon ot tell the client to
// exit with the specified status
const ExitPrefix = "-- exit "

func IsServerRunning() bool {
	conn, err := net.Dial("unix", socketName)
	if err != nil {
		return false
	}
	defer conn.Close()

	data := ClientMessage{
		Args:          []string{"edgectl", "version"},
		APIVersion:    apiVersion,
		ClientVersion: DisplayVersion(),
	}
	encoder := json.NewEncoder(conn)
	if err := encoder.Encode(&data); err != nil {
		return false
	}

	if _, err := ioutil.ReadAll(conn); err != nil {
		return false
	}

	return true
}

func MainViaDaemon() error {
	conn, err := net.Dial("unix", socketName)
	if err != nil {
		return err
	}
	defer conn.Close()

	rai, err := GetRunAsInfo()
	if err != nil {
		return errors.Wrap(err, "failed to get local info")
	}

	// Create or read the install ID here, as the user, and pass it to the
	// daemon, where it will be used to send reports.
	installID := NewScout("unused").Reporter.InstallID()

	data := ClientMessage{
		Args:          os.Args,
		RAI:           rai,
		APIVersion:    apiVersion,
		ClientVersion: DisplayVersion(),
		InstallID:     installID,
	}
	encoder := json.NewEncoder(conn)
	if err := encoder.Encode(&data); err != nil {
		return errors.Wrap(err, "encode/send")
	}

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, ExitPrefix) {
			codeStr := line[len(ExitPrefix):]
			code, err := strconv.Atoi(codeStr)
			if err != nil {
				fmt.Println()
				fmt.Printf("Bad exit code from daemon: %q", codeStr)
				code = 1
			}
			os.Exit(code)
		}
		fmt.Println(scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return err
	}

	os.Exit(0)
	return nil // not reached
}
