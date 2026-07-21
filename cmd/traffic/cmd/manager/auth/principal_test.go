package auth_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
)

func TestPrincipal_SameAs(t *testing.T) {
	tests := []struct {
		name string
		p    *auth.Principal
		o    *auth.Principal
		want bool
	}{
		{"nil p", nil, &auth.Principal{Username: "u"}, false},
		{"nil o", &auth.Principal{Username: "u"}, nil, false},
		{"different usernames", &auth.Principal{Username: "u1", UID: "1"}, &auth.Principal{Username: "u2", UID: "1"}, false},
		{"same username and uid", &auth.Principal{Username: "u", UID: "1"}, &auth.Principal{Username: "u", UID: "1"}, true},
		{"same username, different uid", &auth.Principal{Username: "u", UID: "1"}, &auth.Principal{Username: "u", UID: "2"}, false},
		{"same username, one uid empty", &auth.Principal{Username: "u", UID: ""}, &auth.Principal{Username: "u", UID: "2"}, true},
		{"same username, both uid empty", &auth.Principal{Username: "u"}, &auth.Principal{Username: "u"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.p.SameAs(tt.o))
		})
	}
}

func TestPrincipalFrom_Absent(t *testing.T) {
	assert.Nil(t, auth.PrincipalFrom(context.Background()))
}

func TestWithPrincipal_RoundTrip(t *testing.T) {
	p := &auth.Principal{Username: "u", UID: "1"}
	ctx := auth.WithPrincipal(context.Background(), p)
	assert.Same(t, p, auth.PrincipalFrom(ctx))
}
