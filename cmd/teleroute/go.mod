module github.com/telepresenceio/telepresence/cmd/teleroute

go 1.24.0

require (
	github.com/cenkalti/backoff/v4 v4.3.0
	github.com/docker/go-plugins-helpers v0.0.0-20240701071450-45e2431495c8
	github.com/puzpuzpuz/xsync/v4 v4.2.0
	github.com/sirupsen/logrus v1.9.3
	github.com/telepresenceio/telepresence/rpc/v2 v2.25.2
	google.golang.org/grpc v1.78.0
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/coreos/go-systemd v0.0.0-20191104093116-d3cd4ed1dbcf // indirect
	github.com/docker/go-connections v0.6.0 // indirect
	golang.org/x/net v0.48.0 // indirect
	golang.org/x/sys v0.39.0 // indirect
	golang.org/x/text v0.32.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20251222181119-0a764e51fe1b // indirect
)

replace github.com/telepresenceio/telepresence/rpc/v2 => ./rpc
