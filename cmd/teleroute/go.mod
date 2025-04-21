module github.com/telepresenceio/telepresence/cmd/teleroute

go 1.24

require (
	github.com/docker/go-plugins-helpers v0.0.0-20240701071450-45e2431495c8
	github.com/go-json-experiment/json v0.0.0-20250223041408-d3c622f1b874
	github.com/puzpuzpuz/xsync/v4 v4.0.0
	github.com/sirupsen/logrus v1.9.3
	github.com/telepresenceio/telepresence/rpc/v2 v2.23.0
	github.com/vishvananda/netlink v1.3.0
	google.golang.org/grpc v1.71.1
	google.golang.org/protobuf v1.36.6
)

require (
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/coreos/go-systemd v0.0.0-20191104093116-d3cd4ed1dbcf // indirect
	github.com/docker/go-connections v0.5.0 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/stretchr/testify v1.10.0 // indirect
	github.com/vishvananda/netns v0.0.4 // indirect
	go.opentelemetry.io/otel v1.35.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.35.0 // indirect
	golang.org/x/net v0.39.0 // indirect
	golang.org/x/sys v0.32.0 // indirect
	golang.org/x/text v0.24.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20250409194420-de1ac958c67a // indirect
)

replace github.com/telepresenceio/telepresence/rpc/v2 => ./rpc
