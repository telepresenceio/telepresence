package userd_auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"mime"
	"net/http"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

func getAPIKey(ctx context.Context, env client.Env, accessToken, desc string) (string, error) {
	// Build the request.
	reqBody, err := json.Marshal(map[string]interface{}{
		"description": desc,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx,
		http.MethodPost, fmt.Sprintf("https://%s/sso/api/apikeys", env.LoginDomain),
		bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	// Send the request.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	// From here on out, we should wrap any errors to include the HTTP response status code.

	// Sanity-check the content type before reading the body.
	mimetype, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		return "", fmt.Errorf("http %v: %w", resp.StatusCode, err)
	}
	if mimetype != "application/json" {
		return "", fmt.Errorf("http %v: response body is not json: %q", resp.StatusCode, mimetype)
	}

	// Read the JSON body.  We do this _before_ checking the resp.StatusCode so that we can get
	// the error message out of the body.
	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		// No need to wrap this error with the status code, the magical resp.Body reader
		// includes it for us.
		return "", err
	}
	type keyInfo struct {
		// #/definitions/ApiKey
		ID            string `json:"id"`            // API Key ID, used to identify the resource for listing and deleting keys
		AccountID     string `json:"accountId"`     // Account ID bound to the API Key
		CreatorUserID string `json:"creatorUserId"` // User ID of the API Key's creator
		CreationDate  string `json:"creationDate"`  // Date where the API Key was created
		Description   string `json:"description"`   // Description of the API Key as optionally provided during creation
		Key           string `json:"key"`           // Secret key used to authenticate API Key protected endpoints. Only exposed on API Key creation.

		// #/definitions/Error
		Message *string `json:"message"`
	}
	var body keyInfo
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		return "", fmt.Errorf("http %v: %w", resp.StatusCode, err)
	}
	if body.Message != nil {
		return "", fmt.Errorf("http %v: error content: %q", resp.StatusCode, *body.Message)
	}

	return body.Key, nil
}
