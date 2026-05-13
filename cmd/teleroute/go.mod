module github.com/telepresenceio/telepresence/cmd/teleroute

go 1.25.0

require (
	github.com/cenkalti/backoff/v4 v4.3.0
	github.com/docker/go-plugins-helpers v0.0.0-20240701071450-45e2431495c8
	github.com/puzpuzpuz/xsync/v4 v4.5.0
	github.com/telepresenceio/clog v0.0.0-20260114221933-287514cf9831
	github.com/telepresenceio/telepresence/rpc/v2 v2.27.4
	google.golang.org/grpc v1.81.0
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/coreos/go-systemd v0.0.0-20191104093116-d3cd4ed1dbcf // indirect
	github.com/docker/go-connections v0.7.0 // indirect
	golang.org/x/net v0.54.0 // indirect
	golang.org/x/sys v0.44.0 // indirect
	golang.org/x/text v0.37.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260504160031-60b97b32f348 // indirect
)

replace github.com/telepresenceio/telepresence/rpc/v2 => ./rpc
