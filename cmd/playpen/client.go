package main

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"os"

	"strings"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/json"
)

// GetClient returns an http.Client that can (only) connect to unix sockets
func GetClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				dialer := net.Dialer{}
				return dialer.DialContext(ctx, "unix", socketName)
			},
		},
	}
}

// PPRequest represents a request from the client to the server.
type PPRequest struct {
	Command string
	Args    []string
	Env     []string
	UID     int
}

func newRequest(command string, args ...string) *PPRequest {
	return &PPRequest{command, args, os.Environ(), os.Getuid()}
}

func sendRequest(command string) (int, string, error) {
	client := GetClient()
	req := newRequest(command)
	reqBody, err := json.Marshal(req)
	if err != nil {
		return 0, "", errors.Wrap(err, "JSON of request")
	}
	url := fmt.Sprintf("http://unix/api/v%d/%s", apiVersion, command)
	res, err := client.Post(url, "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return 0, "", err
	}
	body, err := ioutil.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		return res.StatusCode, "", errors.Wrap(err, "request read body")
	}
	return res.StatusCode, string(body), nil
}

func isServerRunning() bool {
	_, _, err := sendRequest("version")
	return err == nil
}

var failedToConnect = fmt.Sprintf(`
Failed to connect to the server. Is it still running? Take a look in %s for more information. You can start the server using "sudo playpen start-server" if it is not running.
`, logfile)

var apiMismatch = fmt.Sprintf(`
Failed to communicate with the server. This is usually due to an API version mismatch. Try "playpen version" to see the client and server versions. If that's not the problem, take a look in %s for more information.
`, logfile)

func doClientRequest(command string) (string, error) {
	statusCode, body, err := sendRequest(command)
	if err != nil {
		fmt.Println(WordWrapString(failedToConnect))
		return "", err
	}
	if statusCode == 404 {
		fmt.Println(WordWrapString(apiMismatch))
		return "", errors.New("could not communicate with server")
	}
	return body, nil
}

func doStatus() error {
	body, err := doClientRequest("status")
	if err != nil {
		return err
	}
	println(body)
	return nil
}

func doConnect() error {
	body, err := doClientRequest("connect")
	if err != nil {
		return err
	}
	println(body)
	return nil
}

func doDisconnect() error {
	body, err := doClientRequest("disconnect")
	if err != nil {
		return err
	}
	println(body)
	return nil
}

func fetchResponse(path string) (string, error) {
	client := GetClient()
	res, err := client.Get(fmt.Sprintf("http://unix/%s", path))
	if err != nil {
		fmt.Println(WordWrapString(failedToConnect))
		return "", err
	}
	body, err := ioutil.ReadAll(res.Body)
	res.Body.Close()
	return string(body), err
}

func doVersion() error {
	fmt.Printf("playpen client %s\n", displayVersion)
	body, err := fetchResponse("version")
	if err != nil {
		return err
	}
	fmt.Println(strings.TrimRight(body, "\n"))
	return nil
}

func doQuit() error {
	body, err := fetchResponse("quit")
	if err != nil {
		return err
	}
	fmt.Println(strings.TrimRight(body, "\n"))
	return nil
}
