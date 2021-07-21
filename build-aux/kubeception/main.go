//+build ignore

package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"time"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

const apiRoot = "https://sw.bakerstreet.io/kubeception/api/klusters/"

func kubeceptionRequest(ctx context.Context, client *http.Client, httpVerb, token, clusterName string, queryParams map[string]string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, httpVerb, apiRoot+clusterName, nil)
	if err != nil {
		return "", fmt.Errorf("Unable to build %s request: %w", httpVerb, err)
	}
	q := req.URL.Query()
	for k, v := range queryParams {
		q.Add(k, v)
	}
	req.URL.RawQuery = q.Encode()
	req.Header.Add("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("Error in request: %w", err)
	}
	defer resp.Body.Close()
	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("Unable to read response body: %w", err)
	}
	body := string(b)
	if resp.StatusCode >= 300 || resp.StatusCode < 200 {
		return "", fmt.Errorf("Status code was %d. Response body: %s", resp.StatusCode, body)
	}
	return body, nil
}

func run() error {
	if len(os.Args) != 3 {
		return fmt.Errorf("Usage: %s <create|destroy> <cluster-name>", os.Args[0])
	}
	verb := os.Args[1]
	clusterName := os.Args[2]

	token, ok := os.LookupEnv("TELEPRESENCE_KUBECEPTION_TOKEN")
	if !ok {
		return fmt.Errorf("Please set the TELEPRESENCE_KUBECEPTION_TOKEN environment variable to a valid kubeception token")
	}
	cli := &http.Client{}
	switch verb {
	case "create":
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		err := client.Retry(ctx, "kubeception", func(ctx context.Context) error {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			kubeconfig, err := kubeceptionRequest(ctx, cli, "PUT", token, clusterName, map[string]string{"wait": "true", "timeoutSecs": "7200", "version": "1.19"})
			if err != nil {
				return err
			}
			fmt.Println(kubeconfig)
			return nil
		}, 2*time.Second, 10*time.Second)
		if err != nil {
			return err
		}
	case "destroy":
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_, err := kubeceptionRequest(ctx, cli, "DELETE", token, clusterName, map[string]string{})
		if err != nil {
			return err
		}
		fmt.Println("Cluster destroyed! Have a nice day.")
	default:
		return fmt.Errorf("Invalid parameter %s, must be one of create or destroy", verb)
	}

	return nil
}

func main() {
	err := run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
