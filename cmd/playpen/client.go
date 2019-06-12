package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"strings"

	"github.com/pkg/errors"
	"github.com/ybbus/jsonrpc"
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

var failedToConnect = fmt.Sprintf(`
Failed to connect to the server. Is it still running? Take a look in %s for more information. You can start the server using "sudo playpen start-server" if it is not running.
`, logfile)

var apiMismatch = fmt.Sprintf(`
Failed to communicate with the server. This is usually due to an API version mismatch. Try "playpen version" to see the client and server versions. If that's not the problem, take a look in %s for more information.
`, logfile)

func doClientRequest(command string, params interface{}) (*jsonrpc.RPCResponse, error) {
	url := fmt.Sprintf("http://unix/api/v%d", apiVersion)
	clientOpts := &jsonrpc.RPCClientOpts{HTTPClient: GetClient()}
	rpcClient := jsonrpc.NewClientWithOpts(url, clientOpts)
	method := fmt.Sprintf("daemon.%s", command)
	response, err := rpcClient.Call(method, params)
	if err != nil {
		httpErr, ok := err.(*jsonrpc.HTTPError)
		if !ok {
			fmt.Println(err)
			fmt.Println("")
			fmt.Println(WordWrapString(failedToConnect))
			return nil, errors.New("unable to connect to server")
		}
		fmt.Println(httpErr)
		fmt.Println("")
		fmt.Println(WordWrapString(apiMismatch))
		return nil, errors.New("could not communicate with server")
	}
	return response, nil
}

func decodeAsStringReply(response *jsonrpc.RPCResponse) (string, error) {
	res := &StringReply{}
	err := response.GetObject(res)
	if err != nil {
		return "", errors.Wrap(err, "bad response from server")
	}
	if len(res.Message) == 0 {
		return "", errors.New("empty message from server")
	}
	return res.Message, nil
}

func doStatus() error {
	response, err := doClientRequest("Status", EmptyArgs{})
	if err != nil {
		return errors.Wrap(err, "Status call")
	}
	message, err := decodeAsStringReply(response)
	if err != nil {
		return errors.Wrap(err, "Status result")
	}

	fmt.Println(message)
	return nil
}

func doConnect() error {
	response, err := doClientRequest("Connect", EmptyArgs{})
	if err != nil {
		return errors.Wrap(err, "Connect call")
	}
	message, err := decodeAsStringReply(response)
	if err != nil {
		return errors.Wrap(err, "Connect result")
	}

	fmt.Println(message)
	return nil
}

func doDisconnect() error {
	response, err := doClientRequest("Disconnect", EmptyArgs{})
	if err != nil {
		return errors.Wrap(err, "Disconnect call")
	}
	message, err := decodeAsStringReply(response)
	if err != nil {
		return errors.Wrap(err, "Disconnect result")
	}

	fmt.Println(message)
	return nil
}

func fetchResponse(path string, verbose bool) (string, error) {
	client := GetClient()
	res, err := client.Get(fmt.Sprintf("http://unix/%s", path))
	if err != nil {
		if verbose {
			fmt.Println(WordWrapString(failedToConnect))
		}
		return "", err
	}
	body, err := ioutil.ReadAll(res.Body)
	res.Body.Close()
	return string(body), err
}

func isServerRunning() bool {
	_, err := fetchResponse("version", false)
	return err == nil
}

func doVersion() error {
	fmt.Printf("playpen client %s\n", displayVersion)
	body, err := fetchResponse("version", true)
	if err != nil {
		return err
	}
	fmt.Println(strings.TrimRight(body, "\n"))
	return nil
}

func doQuit() error {
	body, err := fetchResponse("quit", true)
	if err != nil {
		return err
	}
	fmt.Println(strings.TrimRight(body, "\n"))
	return nil
}
