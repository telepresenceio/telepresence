module github.com/telepresenceio/telepresence/cmd/teleroute

go 1.25

require (
	github.com/cenkalti/backoff/v4 v4.3.0
	github.com/docker/go-plugins-helpers v0.0.0-20240701071450-45e2431495c8
	github.com/puzpuzpuz/xsync/v4 v4.3.0
	github.com/telepresenceio/clog v0.0.0-20260114095906-871c1e5d508d
	github.com/telepresenceio/telepresence/rpc/v2 v2.25.2
	google.golang.org/grpc v1.78.0
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/coreos/go-systemd v0.0.0-20191104093116-d3cd4ed1dbcf // indirect
	github.com/docker/go-connections v0.6.0 // indirect
	golang.org/x/net v0.49.0 // indirect
	golang.org/x/sys v0.40.0 // indirect
	golang.org/x/text v0.33.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260112192933-99fd39fd28a9 // indirect
)

replace github.com/telepresenceio/telepresence/rpc/v2 => ./rpc
