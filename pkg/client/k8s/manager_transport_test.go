package k8s

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

func TestUsesExternalManager(t *testing.T) {
	assert.False(t, (&client.Cluster{}).UsesExternalManager())
	assert.True(t, (&client.Cluster{ManagerAddress: "tls://tm.example.com:8443"}).UsesExternalManager())
}

func TestParseManagerAddress(t *testing.T) {
	tests := []struct {
		name           string
		addr           string
		wantHostPort   string
		wantServerName string
		wantErr        string
	}{
		{
			name:           "valid tls address",
			addr:           "tls://tm.example.com:8443",
			wantHostPort:   "tm.example.com:8443",
			wantServerName: "tm.example.com",
		},
		{
			name:    "unsupported scheme",
			addr:    "grpc://tm.example.com:8443",
			wantErr: `unsupported scheme "grpc"`,
		},
		{
			name:    "missing port",
			addr:    "tls://tm.example.com",
			wantErr: "must include both a host and a port",
		},
		{
			name:    "invalid url",
			addr:    "tls://%zz",
			wantErr: "tls://%zz",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hostPort, serverName, err := parseManagerAddress(tt.addr)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Equal(t, errcat.User, errcat.GetCategory(err))
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantHostPort, hostPort)
			assert.Equal(t, tt.wantServerName, serverName)
		})
	}
}

const testCAPEM = `-----BEGIN CERTIFICATE-----
MIIDAzCCAeugAwIBAgIUa/9dGXKOMWzIjSeA8WhTQn5IbrswDQYJKoZIhvcNAQEL
BQAwETEPMA0GA1UEAwwGdGVzdENBMB4XDTI2MDgwODIyMDE1N1oXDTM2MDgwNTIy
MDE1N1owETEPMA0GA1UEAwwGdGVzdENBMIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8A
MIIBCgKCAQEAvRHIAZy8qSExIC2RfxmnyPD91ZiuCttfN6IBKYm1QCaUcbvoLdn4
EuqW22l1nXrMmQdfxsSJMzQ5faq+/9vy3smPN1TlC2aYqBw9ph0mPdz2t5vbPxls
AVBWSppeBwYVTi+AmII/GK8DO8AC64b9IpLy1bU+oHDLuWMMIcdf/8wIaJC0H45n
Xm4QVaQ90hk+HKRgszbXas70GCYw+lG4ZznroFzaoeidvZZN2qcU1Qih/tXJoZ9a
oKmZQD80H826aT0pZCBFTRfDHIJ2bO86fghHJu8HXbDp5IHTt1YJ+WqbMHAmw/Yo
e26mejbV4kr4hZDONTAnij/GtyPc0sLnSwIDAQABo1MwUTAdBgNVHQ4EFgQUQX/A
1X2AJ48Mj5xIv8MIsLb+WDEwHwYDVR0jBBgwFoAUQX/A1X2AJ48Mj5xIv8MIsLb+
WDEwDwYDVR0TAQH/BAUwAwEB/zANBgkqhkiG9w0BAQsFAAOCAQEAtinCL/rbgpOE
1keCmVRJ1W/cUJE6AXOq5KkPOxJd1Lr5ExgIxZDM374CgvhRppqJdvcdbvUfcdOk
crzqPwwM4RCEAlXwEEKHxymM8VKnElwt460aeEUV5Af6K96PEOYRmvqrjU7wdCaD
R/X32B1ONmftnzN2cUKz44l8MP/JCcZuFoTZgtFGQuRswBTA3oQRzfL6/6OU6Kwy
RUR+vBxRkXo/GUc3/Zkt4vABzwJ9mBFGsfmiFwH6eczZHRNoAmIns8+fcACJWMqZ
vbNq6E39ifpN2HPoO3zGqgEo3gcGLALOckI4deoc/mTvY5HyuTo/cXTOlOgooU5k
D2L3yuUnrA==
-----END CERTIFICATE-----
`

func TestResolveManagerServerCA(t *testing.T) {
	t.Run("literal PEM", func(t *testing.T) {
		data, err := resolveManagerServerCA(testCAPEM)
		require.NoError(t, err)
		assert.Equal(t, testCAPEM, string(data))
	})

	t.Run("file path", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "ca.pem")
		require.NoError(t, os.WriteFile(path, []byte(testCAPEM), 0o600))
		data, err := resolveManagerServerCA(path)
		require.NoError(t, err)
		assert.Equal(t, testCAPEM, string(data))
	})

	t.Run("base64", func(t *testing.T) {
		enc := base64.StdEncoding.EncodeToString([]byte(testCAPEM))
		data, err := resolveManagerServerCA(enc)
		require.NoError(t, err)
		assert.Equal(t, testCAPEM, string(data))
	})

	t.Run("neither PEM, file, nor base64", func(t *testing.T) {
		_, err := resolveManagerServerCA("not valid base64 !!!")
		require.Error(t, err)
		assert.Equal(t, errcat.User, errcat.GetCategory(err))
	})
}

func TestManagerServerCredentials(t *testing.T) {
	t.Run("no CA: system trust roots", func(t *testing.T) {
		creds, err := managerServerCredentials("tm.example.com", "", nil)
		require.NoError(t, err)
		require.NotNil(t, creds)
	})

	t.Run("valid CA PEM", func(t *testing.T) {
		creds, err := managerServerCredentials("tm.example.com", testCAPEM, nil)
		require.NoError(t, err)
		require.NotNil(t, creds)
	})

	t.Run("invalid CA content", func(t *testing.T) {
		_, err := managerServerCredentials("tm.example.com", "-----BEGIN CERTIFICATE-----\nbogus\n-----END CERTIFICATE-----\n", nil)
		require.Error(t, err)
		assert.Equal(t, errcat.User, errcat.GetCategory(err))
	})
}
