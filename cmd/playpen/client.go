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

type PPRequest struct {
	APIVersion int
	Command    string
	Args       []string
	Env        []string
	UID        int
}

func newRequest(command string, args ...string) *PPRequest {
	return &PPRequest{apiVersion, command, args, os.Environ(), os.Getuid()}
}

func sendRequest(command string) (string, error) {
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
		return "", errors.Wrap(err, "JSON of request")
	}
	url := fmt.Sprintf("http://unix/%s", command)
	res, err := client.Post(url, "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return "", errors.Wrap(err, "request")
	}
	body, err := ioutil.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		return "", errors.Wrap(err, "request read body")
	}
	return string(body), nil
}

func doClientRequest(command string) string {
	body, err := sendRequest(command)
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
	return body
}

func doStatus() {
	body := doClientRequest("status")
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
