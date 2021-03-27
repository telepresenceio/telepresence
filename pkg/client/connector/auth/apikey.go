package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

func getAPIKey(ctx context.Context, env client.Env, accessToken, desc string) (string, error) {
	authority := net.JoinHostPort(env.SystemAHost, env.SystemAPort)

	reqBody, err := json.Marshal(map[string]interface{}{
		"body": map[string]interface{}{
			"description": desc,
		},
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx,
		http.MethodPost, fmt.Sprintf("https://%s/apikeys", authority),
		bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	type keyInfo struct {
		ID            string `json:"id"`            // API Key ID, used to identify the resource for listing and deleting keys
		AccountID     string `json:"accountId"`     // Account ID bound to the API Key
		CreatorUserID string `json:"creatorUserId"` // User ID of the API Key's creator
		CreationDate  string `json:"creationDate"`  // Date where the API Key was created
		Description   string `json:"description"`   // Description of the API Key as optionally provided during creation
		Key           string `json:"key"`           // Secret key used to authenticate API Key protected endpoints. Only exposed on API Key creation.
	}
	var body keyInfo
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		return "", err
	}

	return body.Key, nil
}
