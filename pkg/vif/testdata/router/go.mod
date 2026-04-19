module local

go 1.26

require (
	github.com/telepresenceio/clog v0.0.0-20260114221933-287514cf9831
	github.com/telepresenceio/telepresence/v2 v2.27.4
)

require (
	github.com/blang/semver/v4 v4.0.0 // indirect
	github.com/caarlos0/env/v11 v11.4.0 // indirect
	github.com/cenkalti/backoff/v4 v4.3.0 // indirect
	github.com/fsnotify/fsnotify v1.9.0 // indirect
	github.com/fxamacker/cbor/v2 v2.9.1 // indirect
	github.com/go-json-experiment/json v0.0.0-20260214004413-d219187c3433 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/google/btree v1.1.3 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.3-0.20250322232337-35a7c28c31ee // indirect
	github.com/puzpuzpuz/xsync/v4 v4.5.0 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	github.com/telepresenceio/telepresence/rpc/v2 v2.28.0-rc.1 // indirect
	github.com/vishvananda/netlink v1.3.1 // indirect
	github.com/vishvananda/netns v0.0.5 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	go.yaml.in/yaml/v2 v2.4.4 // indirect
	golang.org/x/exp v0.0.0-20260410095643-746e56fc9e2f // indirect
	golang.org/x/net v0.53.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/text v0.36.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
	golang.zx2c4.com/wireguard v0.0.0-20250521234502-f333402bd9cb // indirect
	golang.zx2c4.com/wireguard/windows v0.6.1 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260414002931-afd174a4e478 // indirect
	google.golang.org/grpc v1.80.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gvisor.dev/gvisor v0.0.0-20260416192914-3278e925778d // indirect
	k8s.io/api v0.35.4 // indirect
	k8s.io/apimachinery v0.35.4 // indirect
	k8s.io/klog/v2 v2.140.0 // indirect
	k8s.io/kube-openapi v0.0.0-20260414162039-ec9c827d403f // indirect
	k8s.io/utils v0.0.0-20260319190234-28399d86e0b5 // indirect
	sigs.k8s.io/json v0.0.0-20250730193827-2d320260d730 // indirect
	sigs.k8s.io/randfill v1.0.0 // indirect
	sigs.k8s.io/structured-merge-diff/v6 v6.4.0 // indirect
	sigs.k8s.io/yaml v1.6.0 // indirect
)

replace github.com/telepresenceio/telepresence/rpc/v2 => ./../../../../rpc

replace github.com/telepresenceio/telepresence/cmd/cobraparser/v2 => ./../../../../cmd/cobraparser

replace github.com/telepresenceio/telepresence/v2 => ./../../../..
