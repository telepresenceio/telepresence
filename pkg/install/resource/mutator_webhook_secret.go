package resource

import (
	"context"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
)

type mwhSecret int

const MutatorWebhookSecret = mwhSecret(0)

var _ Instance = MutatorWebhookSecret

func (ri mwhSecret) secret(ctx context.Context) *kates.Secret {
	sec := new(kates.Secret)
	sec.TypeMeta = kates.TypeMeta{
		Kind:       "Secret",
		APIVersion: "v1",
	}
	sec.ObjectMeta = kates.ObjectMeta{
		Namespace: getScope(ctx).namespace,
		Name:      install.MutatorWebhookTLSName,
	}
	return sec
}

func (ri mwhSecret) Create(ctx context.Context) (err error) {
	sc := getScope(ctx)
	if sc.crtPem, sc.keyPem, sc.caPem, err = install.GenerateKeys(sc.namespace); err != nil {
		return err
	}
	sec := ri.secret(ctx)
	sec.Data = map[string][]byte{
		"crt.pem": sc.crtPem,
		"key.pem": sc.keyPem,
		"ca.pem":  sc.caPem,
	}
	return create(ctx, sec)
}

func (ri mwhSecret) Exists(ctx context.Context) (bool, error) {
	return exists(ctx, ri.secret(ctx))
}

func (ri mwhSecret) Delete(ctx context.Context) error {
	return remove(ctx, ri.secret(ctx))
}

func (ri mwhSecret) Update(_ context.Context) error {
	// Noop
	return nil
}
