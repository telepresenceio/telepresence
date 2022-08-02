package tracing_test

import (
	"bytes"
	"context"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/tracing"
)

func Test_Serde(t *testing.T) {
	buf := &bytes.Buffer{}
	wr := tracing.NewProtoWriter(buf)
	// Really any old proto will do here
	users := []*connector.UserInfo{
		{
			Id:               "1",
			Name:             "socrates",
			AvatarUrl:        "socrates.com",
			AccountId:        "123",
			AccountName:      "foobar",
			AccountAvatarUrl: "foobar.com",
			Email:            "socrates@socrates.com",
		},
		{
			Id:               "2",
			Name:             "abc",
			AvatarUrl:        "abc.com",
			AccountId:        "123",
			AccountName:      "foobar",
			AccountAvatarUrl: "abc.com",
			Email:            "abc@abc.com",
		},
		{
			Id:               "3",
			Name:             "aristotle",
			AvatarUrl:        "aristotle.com",
			AccountId:        "123",
			AccountName:      "foobar",
			AccountAvatarUrl: "foobar.com",
			Email:            "aristotle@aristotle.com",
		},
		{
			Id:               "4",
			Name:             "plato",
			AvatarUrl:        "plato.com",
			AccountId:        "123",
			AccountName:      "foobar",
			AccountAvatarUrl: "foobar.com",
			Email:            "plato@plato.com",
		},
	}
	for _, user := range users {
		err := wr.Encode(user)
		if err != nil {
			t.Fatal(err)
		}
	}
	rr := tracing.NewProtoReader(buf, func() *connector.UserInfo { return new(connector.UserInfo) })
	results, err := rr.ReadAll(context.TODO())
	if err != nil {
		t.Fatal(err)
	}
	for i, result := range results {
		if !proto.Equal(result, users[i]) {
			t.Fatalf("Deserialization failed, expected %+v, got %+v", users[i], result)
		}
	}
}
