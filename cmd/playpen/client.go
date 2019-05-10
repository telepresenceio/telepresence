package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"os"

	"bytes"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/json"
)

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
	client := http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				dialer := net.Dialer{}
				return dialer.DialContext(ctx, "unix", socketName)
			},
		},
	}
	req := newRequest(command)
	reqBody, err := json.Marshal(req)
	if err != nil {
		return 0, "", errors.Wrap(err, "JSON of request")
	}
	url := fmt.Sprintf("http://unix/api/v%d/%s", apiVersion, command)
	res, err := client.Post(url, "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return 0, "", errors.Wrap(err, "request")
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

func doClientRequest(command string) string {
	statusCode, body, err := sendRequest(command)
	if err != nil {
		fmt.Println(err)
		fmt.Println()
		fmt.Println("Failed to connect to the server. Is it still running?")
		fmt.Printf("Take a look in %s for more information.\n", logfile)
		fmt.Println("You can start the server using playpen start-server.")
		fmt.Println()
		fmt.Println("playpen: Could not connect to server")
		os.Exit(1)
	}
	if statusCode == 404 {
		fmt.Println("Failed to communicate with the server. This is usually")
		fmt.Println("due to an API version mismatch. Take a look at the start")
		fmt.Printf("of %s for the server version.\n", logfile)
		fmt.Printf("The client is playpen client %s\n", displayVersion)
		fmt.Println()
		fmt.Println("playpen: Could not communicate with server")
		os.Exit(1)
	}
	return body
}

func doStatus() {
	body := doClientRequest("status")
	println(body)
}

func doConnect() {
	body := doClientRequest("connect")
	println(body)
}

func doDisconnect() {
	body := doClientRequest("disconnect")
	println(body)
}

func doVersion() {
	body := doClientRequest("version")
	println(body)
}

func doQuit() {
	body := doClientRequest("quit")
	println(body)
}
