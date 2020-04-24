package edgectl

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/pkg/errors"

	"github.com/datawire/ambassador/internal/pkg/edgectl"
)

var validEmailAddress = regexp.MustCompile("^[a-zA-Z0-9.!#$%&'*+/=?^_`{|}~-]+@[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(?:\\.[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$")

func getEmailAddress(defaultEmail string, log *log.Logger) string {
	prompt := fmt.Sprintf("Email address [%s]: ", defaultEmail)
	errorFallback := defaultEmail
	if defaultEmail == "" {
		prompt = "Email address: "
		errorFallback = "email_query_failure@datawire.io"
	}

	for {
		fmt.Print(prompt)
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Scan()
		text := scanner.Text()
		if err := scanner.Err(); err != nil {
			log.Printf("Email query failed: %+v", err)
			return errorFallback
		}

		text = strings.TrimSpace(text)
		if defaultEmail != "" && text == "" {
			return defaultEmail
		}

		if validEmailAddress.MatchString(text) {
			return text
		}

		fmt.Printf("Sorry, %q does not appear to be a valid email address.  Please check it and try again.\n", text)
	}
}

// LoopFailedError is a fatal error for loopUntil(...)
type LoopFailedError string

// Error implements error
func (s LoopFailedError) Error() string {
	return string(s)
}

type loopConfig struct {
	sleepTime    time.Duration // How long to sleep between calls
	progressTime time.Duration // How long until we explain why we're waiting
	timeout      time.Duration // How long until we give up
}

var lc2 = &loopConfig{
	sleepTime:    500 * time.Millisecond,
	progressTime: 15 * time.Second,
	timeout:      120 * time.Second,
}

var lc5 = &loopConfig{
	sleepTime:    3 * time.Second,
	progressTime: 30 * time.Second,
	timeout:      5 * time.Minute,
}

// loopUntil repeatedly calls a function until it succeeds, using a
// (presently-fixed) loop period and timeout.
func (i *Installer) loopUntil(what string, how func() error, lc *loopConfig) error {
	ctx, cancel := context.WithTimeout(i.ctx, lc.timeout)
	defer cancel()
	start := time.Now()
	i.log.Printf("Waiting for %s", what)
	defer func() { i.log.Printf("Wait for %s took %.1f seconds", what, time.Since(start).Seconds()) }()
	progTimer := time.NewTimer(lc.progressTime)
	defer progTimer.Stop()
	for {
		err := how()
		if err == nil {
			return nil // Success
		} else if _, ok := err.(LoopFailedError); ok {
			return err // Immediate failure
		}
		// Wait and try again
		select {
		case <-progTimer.C:
			i.ShowWaiting(what)
		case <-time.After(lc.sleepTime):
			// Try again
		case <-ctx.Done():
			i.ShowTimedOut(what)
			return errors.Errorf("timed out waiting for %s (or interrupted)", what)
		}
	}
}

// ShowWrapped displays to the user (via the show logger) the text items passed
// in with word wrapping applied. Leading and trailing newlines are dropped in
// each text item (to make it easier to use multiline constants), but newlines
// within each item are preserved. Use an empty string item to include a blank
// line in the output between other items.
func (i *Installer) ShowWrapped(texts ...string) {
	for _, text := range texts {
		text = strings.Trim(text, "\n")                  // Drop leading and trailing newlines
		for _, para := range strings.Split(text, "\n") { // Preserve newlines in the text
			for _, line := range doWordWrap(para, "", 79) { // But wrap text too
				i.show.Println(line)
			}
		}
	}
}

func doWordWrap(text string, prefix string, lineWidth int) []string {
	words := strings.Fields(strings.TrimSpace(text))
	if len(words) == 0 {
		return []string{""}
	}
	lines := make([]string, 0)
	wrapped := prefix + words[0]
	for _, word := range words[1:] {
		if len(word)+1 > lineWidth-len(wrapped) {
			lines = append(lines, wrapped)
			wrapped = prefix + word
		} else {
			wrapped += " " + word
		}
	}
	if len(wrapped) > 0 {
		lines = append(lines, wrapped)
	}
	return lines
}

// Capture calls a command and returns its stdout
func (i *Installer) Capture(name string, logToStdout bool, input string, args ...string) (res string, err error) {
	res = ""
	resAsBytes := &bytes.Buffer{}
	i.log.Printf("$ %s", strings.Join(args, " "))
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin = strings.NewReader(input)
	if logToStdout {
		cmd.Stdout = io.MultiWriter(edgectl.NewLoggingWriter(i.cmdOut), resAsBytes)
	} else {
		cmd.Stdout = resAsBytes
	}
	cmd.Stderr = edgectl.NewLoggingWriter(i.cmdErr)
	err = cmd.Run()
	if err != nil {
		err = errors.Wrap(err, name)
	}
	res = resAsBytes.String()
	return
}
