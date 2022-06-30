package license

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/square/go-jose.v2"
	"gopkg.in/square/go-jose.v2/jwt"
)

func TestNewBundleFromDisk(t *testing.T) {
	tmpRootDir, err := os.MkdirTemp("", "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpRootDir) }()
	expectErrorTest := func(t *testing.T) {
		l, err := LoadBundle(tmpRootDir)
		if err == nil {
			t.Errorf("expected error while reading license from disk")
		}
		if l != nil {
			t.Errorf("expected nil license")
		}
	}

	t.Run("no license file", expectErrorTest)

	err = os.WriteFile(filepath.Join(tmpRootDir, "license"), []byte(""), os.ModePerm)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("empty license file", expectErrorTest)

	expectedLicense := "LICENSE"
	err = os.WriteFile(filepath.Join(tmpRootDir, "license"), []byte(expectedLicense), os.ModePerm)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("empty license file", expectErrorTest)

	err = os.WriteFile(filepath.Join(tmpRootDir, "hostDomain"), []byte(""), os.ModePerm)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("empty host domain file", expectErrorTest)

	expectedHostDomain := "HOST"
	err = os.WriteFile(filepath.Join(tmpRootDir, "hostDomain"), []byte(expectedHostDomain), os.ModePerm)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("no errors", func(t *testing.T) {
		lb, err := LoadBundle(tmpRootDir)
		if err != nil {
			t.Errorf("unexpected error while reading license from disk: %s", err.Error())
		}
		if lb.license != expectedLicense {
			t.Errorf("unexpected license: %s", lb.license)
		}
		if lb.host != expectedHostDomain {
			t.Errorf("unexpected license: %s", lb.license)
		}
	})
}

func newTestBundle(host, clusterID string) (*Bundle, error) {
	privKey, pubKey, err := genKeys()
	if err != nil {
		return nil, err
	}
	l := Bundle{
		host: host,
		pubKeys: map[string]string{
			host: pubKey,
		},
	}

	l.license, err = genLicense(privKey, clusterID)
	if err != nil {
		return nil, err
	}

	return &l, nil
}

func TestBundle_extractLicenseClaims(t *testing.T) {
	t.Run("nil license", func(t *testing.T) {
		var lb *Bundle
		claims, err := lb.GetLicenseClaims()
		require.Error(t, err)
		require.Nil(t, claims)
	})

	t.Run("default pubkeys", func(t *testing.T) {
		var lb Bundle
		_, _ = lb.GetLicenseClaims()
		require.Equal(t, lb.pubKeys, pubKeys)
	})

	host := "test-auth.datawire.io"
	t.Run("non-PEM pub key", func(t *testing.T) {
		lb := Bundle{
			host: host,
			pubKeys: map[string]string{
				host: "INVALID KEY",
			},
		}
		_, err := lb.GetLicenseClaims()
		require.Error(t, err)
	})

	t.Run("non-PKIX pub key", func(t *testing.T) {
		lb := Bundle{
			host: host,
			pubKeys: map[string]string{
				host: "-----BEGIN TEST-----\n-----END TEST-----",
			},
		}
		_, err := lb.GetLicenseClaims()
		require.Error(t, err)
	})

	clusterID := "582656ff-054c-474d-841f-a94c6282f9e7"
	t.Run("non-JWT license", func(t *testing.T) {
		lb, err := newTestBundle(host, clusterID)
		require.NoError(t, err)

		lb.license = "INVALID"

		_, err = lb.GetLicenseClaims()
		require.Error(t, err)
	})

	t.Run("no errors", func(t *testing.T) {
		lb, err := newTestBundle(host, clusterID)
		require.NoError(t, err)

		claims, err := lb.GetLicenseClaims()
		require.NoError(t, err)

		require.Contains(t, claims.Audience, clusterID)
		require.False(t, claims.Expiry.Time().IsZero())
	})
}

// genKeys generates a private key to be used for creating a
// jwt token for testing.
func genKeys() (*rsa.PrivateKey, string, error) {
	reader := rand.Reader
	bitSize := 2048
	privateKey, err := rsa.GenerateKey(reader, bitSize)
	if err != nil {
		return nil, "", err
	}

	pkBytes, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return nil, "", err
	}
	pem := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PUBLIC KEY",
		Bytes: pkBytes,
	})
	return privateKey, string(pem), nil
}

func genLicense(key *rsa.PrivateKey, clusterID string) (string, error) {
	signer, err := jose.NewSigner(jose.SigningKey{
		Algorithm: jose.RS256,
		Key:       key,
	}, nil)
	if err != nil {
		return "", err
	}

	claims := make(map[string]any)
	claims["aud"] = []string{clusterID}

	// Generate expiration date
	timeNow := time.Now()
	dayOffset := 1
	expTime := timeNow.AddDate(0, 0, dayOffset)
	expTimeDate := jwt.NewNumericDate(expTime)
	claims["exp"] = expTimeDate

	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signedPayload, err := signer.Sign(payload)
	if err != nil {
		return "", err
	}
	license, err := signedPayload.CompactSerialize()
	if err != nil {
		return "", err
	}
	return license, nil
}

func TestLicenceClaims_isValidForCluster(t *testing.T) {
	clusterID := "582656ff-054c-474d-841f-a94c6282f9e7"
	t.Run("expired", func(t *testing.T) {
		lc := LicenseClaims{
			Claims: jwt.Claims{
				Expiry: jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)),
			},
		}
		err := lc.IsValidForCluster(clusterID)
		require.Error(t, err)
	})

	t.Run("wrong cluster id", func(t *testing.T) {
		lc := LicenseClaims{
			Claims: jwt.Claims{
				Expiry:   jwt.NewNumericDate(time.Now().Add(time.Hour)),
				Audience: jwt.Audience{"INVALID"},
			},
		}
		err := lc.IsValidForCluster(clusterID)
		require.Error(t, err)
	})

	t.Run("no errors", func(t *testing.T) {
		lc := LicenseClaims{
			Claims: jwt.Claims{
				Expiry:   jwt.NewNumericDate(time.Now().Add(time.Hour)),
				Audience: jwt.Audience{clusterID},
			},
		}
		err := lc.IsValidForCluster(clusterID)
		require.NoError(t, err)
	})
}

func TestBundle_GetLicenseInfo(t *testing.T) {
	host := "test-auth.datawire.io"
	canConnectCloud := true
	systemaURL := "SYSTEMAURL"
	clusterID := "582656ff-054c-474d-841f-a94c6282f9e7"
	lb, err := newTestBundle(host, clusterID)
	require.NoError(t, err)

	li := lb.GetLicenseInfo(clusterID, canConnectCloud, systemaURL)
	require.NotNil(t, li)
	require.Equal(t, li.CanConnectCloud, canConnectCloud)
	require.Equal(t, li.SystemaURL, systemaURL)
	require.NoError(t, li.LicenseErr)
	require.NotNil(t, li.Claims)
}

func TestLicenseClaims_GetCLusterID(t *testing.T) {
	t.Run("no error", func(t *testing.T) {
		expectedClusterID := "pr38253v40"
		claims := LicenseClaims{
			Claims: jwt.Claims{
				Audience: []string{expectedClusterID},
			},
		}

		clusterID, err := claims.GetClusterID()
		require.NoError(t, err)
		require.Equal(t, clusterID, expectedClusterID)
	})

	t.Run("empty claims", func(t *testing.T) {
		claims := LicenseClaims{
			Claims: jwt.Claims{},
		}

		clusterID, err := claims.GetClusterID()
		require.Error(t, err)
		require.Equal(t, clusterID, "")
	})
}
