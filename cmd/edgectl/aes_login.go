package main

import (
	"fmt"
	"time"

	"github.com/dgrijalva/jwt-go"
	"github.com/pkg/browser"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

type LoginClaimsV1 struct {
	LoginTokenVersion  string `json:"login_token_version"`
	jwt.StandardClaims `json:",inline"`
}

func aesLogin(_ *cobra.Command, _ []string) error {
	// Obtain signing key
	// -> kubectl -n ambassador get secret ambassador-internal -o json
	signingKey := []byte("1234")

	// Figure out the correct hostname
	// -> kubectl -n ambassador get host -o json
	// and use the oldest host (for now)
	hostname := "FIXME"

	// Construct claims
	now := time.Now()
	duration := 30 * time.Minute
	claims := &LoginClaimsV1{
		"v1",
		jwt.StandardClaims{
			IssuedAt:  now.Unix(),
			NotBefore: now.Unix(),
			ExpiresAt: (now.Add(duration)).Unix(),
		},
	}

	// Generate JWT
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString(signingKey)
	if err != nil {
		return errors.Wrap(err, "Unexpected error generating JWT")
	}

	// Output
	url := fmt.Sprintf("https://%s/edge_stack/admin#%s\n", hostname, tokenString)

	if err := browser.OpenURL(url); err != nil {
		fmt.Println("Unexpected error while trying to open your browser.")
		fmt.Println("Visit the following URL to access the Ambassador Edge Stack admin UI:")
		fmt.Println("    ", url)
		return errors.Wrap(err, "browse")
	}
	fmt.Println("Ambassador Edge Stack admin UI has been opened in your browser.")
	return nil
}
