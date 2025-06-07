module github.com/telepresenceio/telepresence/cmd/teleroute

go 1.24

require (
	github.com/cenkalti/backoff/v4 v4.3.0
	github.com/docker/go-plugins-helpers v0.0.0-20240701071450-45e2431495c8
	github.com/puzpuzpuz/xsync/v4 v4.1.0
	github.com/sirupsen/logrus v1.9.3
	github.com/telepresenceio/telepresence/rpc/v2 v2.23.0
	google.golang.org/grpc v1.72.2
	google.golang.org/protobuf v1.36.6
)

require (
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/coreos/go-systemd v0.0.0-20191104093116-d3cd4ed1dbcf // indirect
	github.com/docker/go-connections v0.5.0 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/stretchr/testify v1.8.2 // indirect
	golang.org/x/net v0.40.0 // indirect
	golang.org/x/sys v0.33.0 // indirect
	golang.org/x/text v0.25.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20250528174236-200df99c418a // indirect
)

replace github.com/telepresenceio/telepresence/rpc/v2 => ./rpc
