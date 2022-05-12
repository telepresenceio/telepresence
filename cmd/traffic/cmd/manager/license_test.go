package manager

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"gopkg.in/square/go-jose.v2"
	"gopkg.in/square/go-jose.v2/jwt"
	corev1 "k8s.io/api/core/v1"
)

func TestNewLicenseFromDisk(t *testing.T) {
	tmpRootDir, err := ioutil.TempDir("", "")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpRootDir)
	expectErrorTest := func(t *testing.T) {
		l, err := newLicenseFromDisk(tmpRootDir)
		if err == nil {
			t.Errorf("expected error while reading license from disk")
		}
		if l != nil {
			t.Errorf("expected nil license")
		}
	}

	t.Run("no license file", expectErrorTest)

	err = ioutil.WriteFile(filepath.Join(tmpRootDir, "license"), []byte(""), os.ModePerm)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("empty license file", expectErrorTest)

	expectedLicense := "LICENSE"
	err = ioutil.WriteFile(filepath.Join(tmpRootDir, "license"), []byte(expectedLicense), os.ModePerm)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("empty license file", expectErrorTest)

	err = ioutil.WriteFile(filepath.Join(tmpRootDir, "hostDomain"), []byte(""), os.ModePerm)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("empty host domain file", expectErrorTest)

	expectedHostDomain := "HOST"
	err = ioutil.WriteFile(filepath.Join(tmpRootDir, "hostDomain"), []byte(expectedHostDomain), os.ModePerm)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("no errors", func(t *testing.T) {
		l, err := newLicenseFromDisk(tmpRootDir)
		if err != nil {
			t.Errorf("unexpected error while reading license from disk: %s", err.Error())
		}
		if l.license != expectedLicense {
			t.Errorf("unexpected license: %s", l.license)
		}
		if l.host != expectedHostDomain {
			t.Errorf("unexpected license: %s", l.license)
		}
	})
}

func newTestLicense(host, clusterID string) (*license, error) {
	privKey, pubKey, err := genKeys()
	if err != nil {
		return nil, err
	}
	l := license{
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

func TestLicense_getClaims(t *testing.T) {
	t.Run("nil license", func(t *testing.T) {
		var l *license
		claims, err := l.getClaims(context.Background())
		require.Error(t, err)
		require.Nil(t, claims)
	})

	t.Run("default pubkeys", func(t *testing.T) {
		var l license
		_, _ = l.getClaims(context.Background())
		require.Equal(t, l.pubKeys, pubKeys)
	})

	host := "test-auth.datawire.io"
	t.Run("non-PEM pub key", func(t *testing.T) {
		l := license{
			host: host,
			pubKeys: map[string]string{
				host: "INVALID KEY",
			},
		}
		_, err := l.getClaims(context.Background())
		require.Error(t, err)
	})

	t.Run("non-PKIX pub key", func(t *testing.T) {
		l := license{
			host: host,
			pubKeys: map[string]string{
				host: "-----BEGIN TEST-----\n-----END TEST-----",
			},
		}
		_, err := l.getClaims(context.Background())
		require.Error(t, err)
	})

	clusterID := "582656ff-054c-474d-841f-a94c6282f9e7"
	t.Run("non-JWT license", func(t *testing.T) {
		l, err := newTestLicense(host, clusterID)
		require.NoError(t, err)

		l.license = "INVALID"

		_, err = l.getClaims(context.Background())
		require.Error(t, err)
	})

	t.Run("no errors", func(t *testing.T) {
		l, err := newTestLicense(host, clusterID)
		require.NoError(t, err)

		claims, err := l.getClaims(context.Background())
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

	claims := make(map[string]interface{})
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

type mockClusterInfo struct {
	clusterID string
}

func (*mockClusterInfo) Watch(context.Context, rpc.Manager_WatchClusterInfoServer) error {
	panic("(*mockClusterInfo)Watch is unimplemented")
}

func (mci *mockClusterInfo) GetClusterID() string {
	return mci.clusterID
}

func (*mockClusterInfo) GetTrafficManagerPods(context.Context) ([]*corev1.Pod, error) {
	panic("(*mockClusterInfo)GetTrafficManagerPods is unimplemented")
}

func (*mockClusterInfo) GetTrafficAgentPods(context.Context, string) ([]*corev1.Pod, error) {
	panic("(*mockClusterInfo)GetTrafficAgentPods is unimplemented")
}

func TestLicenceClaims_isValidForCluster(t *testing.T) {
	mci := mockClusterInfo{
		clusterID: "582656ff-054c-474d-841f-a94c6282f9e7",
	}

	t.Run("expired", func(t *testing.T) {
		lc := licenseClaims{
			Claims: jwt.Claims{
				Expiry: jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)),
			},
		}
		valid, err := lc.isValidForCluster(&mci)
		require.Error(t, err)
		require.False(t, valid)
	})

	t.Run("wrong cluster id", func(t *testing.T) {
		lc := licenseClaims{
			Claims: jwt.Claims{
				Expiry:   jwt.NewNumericDate(time.Now().Add(time.Hour)),
				Audience: jwt.Audience{"INVALID"},
			},
		}
		valid, err := lc.isValidForCluster(&mci)
		require.Error(t, err)
		require.False(t, valid)
	})

	t.Run("no errors", func(t *testing.T) {
		lc := licenseClaims{
			Claims: jwt.Claims{
				Expiry:   jwt.NewNumericDate(time.Now().Add(time.Hour)),
				Audience: jwt.Audience{mci.clusterID},
			},
		}
		valid, err := lc.isValidForCluster(&mci)
		require.NoError(t, err)
		require.True(t, valid)
	})
}
