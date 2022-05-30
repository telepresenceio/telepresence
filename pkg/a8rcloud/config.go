package a8rcloud

import (
	"crypto/x509"
	"fmt"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func certsFromConfig(cfg *manager.AmbassadorCloudConfig) (*x509.CertPool, error) {
	if cfg.GetProxyCa() == nil {
		return nil, nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(cfg.GetProxyCa()) {
		return nil, fmt.Errorf("not all certs could be loaded from the PEM file provided")
	}
	return pool, nil
}
