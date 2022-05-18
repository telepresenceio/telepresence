package license

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/square/go-jose.v2/jwt"

	"github.com/datawire/dlib/dlog"
)

var ClusterIDZero = "00000000-0000-0000-0000-000000000000"

type Info struct {
	ValidLicense bool
	LicenseErr   error
	Claims       *LicenseClaims

	CanConnectCloud bool
	SystemaURL      string
}

type Bundle struct {
	license string
	host    string
	pubKeys map[string]string
}

func LoadBundle(rootDir string) (*Bundle, error) {
	var lb Bundle

	buf, err := os.ReadFile(filepath.Join(rootDir, "license"))
	if err != nil {
		return nil, fmt.Errorf("error reading license: %w", err)
	}
	if lb.license = string(buf); lb.license == "" {
		return nil, fmt.Errorf("license is empty")
	}

	buf, err = os.ReadFile(filepath.Join(rootDir, "hostDomain"))
	if err != nil {
		return nil, fmt.Errorf("error reading hostDomain: %w", err)
	}
	if lb.host = string(buf); lb.host == "" {
		return nil, fmt.Errorf("host domain is empty")
	}

	return &lb, nil
}

func (b *Bundle) License() string {
	return b.license
}

func (b *Bundle) Host() string {
	return b.host
}

func (l *Bundle) GetLicenseClaims() (*LicenseClaims, error) {
	if l == nil {
		return nil, fmt.Errorf("no license available")
	}

	if len(l.pubKeys) == 0 {
		l.pubKeys = pubKeys
	}

	hostKey, ok := l.pubKeys[l.host]
	if !ok {
		return nil, fmt.Errorf("unknown host")
	}

	block, _ := pem.Decode([]byte(hostKey))
	if block == nil {
		return nil, fmt.Errorf("no PEM data found for public key")
	}

	pubKey, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}

	token, err := jwt.ParseSigned(l.license)
	if err != nil {
		return nil, fmt.Errorf("failed to parse license: %w", err)
	}

	var claims LicenseClaims
	return &claims, token.Claims(pubKey, &claims)
}

func (l *Bundle) GetLicenseInfo(clusterID string, canConnectCloud bool, systemaURL string) *Info {
	info := Info{
		CanConnectCloud: canConnectCloud,
		SystemaURL:      systemaURL,
	}

	info.Claims, info.LicenseErr = l.GetLicenseClaims()
	if info.LicenseErr != nil {
		return &info
	}

	info.LicenseErr = info.Claims.IsValidForCluster(clusterID)
	info.ValidLicense = info.LicenseErr == nil

	return &info
}

type LicenseClaims struct {
	jwt.Claims
	AccountID string      `json:"accountId"`
	Limits    interface{} `json:"limits"`
	Scope     string      `json:"scope"`
}

func (lc *LicenseClaims) IsValidForCluster(cid string) error {
	expiry := lc.Expiry
	if expiry != nil && time.Now().After(expiry.Time()) {
		return errors.New("license has expired")
	}

	claims := lc.Claims
	if !claims.Audience.Contains(cid) {
		return fmt.Errorf("license is for cluster(s) with these UIDs: %v. This cluster has ID: %s", claims.Audience, cid)
	}

	return nil
}

func (lc *LicenseClaims) GetClusterID() (string, error) {
	n := len(lc.Audience)
	if n == 1 {
		return lc.Audience[0], nil
	}

	return "", fmt.Errorf("found %d audience claims", n)
}

type licenseKey struct{}

func WithBundle(ctx context.Context, licenseDir string) context.Context {
	b, err := LoadBundle(licenseDir)
	if err != nil {
		dlog.Infof(ctx, "unable to load license: %v", err)
		return ctx
	}

	return context.WithValue(ctx, licenseKey{}, b)
}

func BundleFromContext(ctx context.Context) *Bundle {
	b, _ := ctx.Value(licenseKey{}).(*Bundle)
	return b
}
