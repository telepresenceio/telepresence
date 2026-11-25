module local

go 1.27.3

require (
	github.com/telepresenceio/clog v0.0.0-20260114221933-287514cf9831
	github.com/telepresenceio/telepresence/v2 v2.33.4
)

require (
	github.com/blang/semver/v4 v4.0.0 // indirect
	github.com/caarlos0/env/v11 v11.4.2 // indirect
	github.com/cenkalti/backoff/v4 v4.3.0 // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/fxamacker/cbor/v2 v2.10.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/godbus/dbus/v5 v5.2.4 // indirect
	github.com/google/btree v1.1.3 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.3-0.20250322232337-35a7c28c31ee // indirect
	github.com/puzpuzpuz/xsync/v4 v4.5.0 // indirect
	github.com/quic-go/quic-go v0.66.0 // indirect
	github.com/rogpeppe/go-internal v1.16.0 // indirect
	github.com/telepresenceio/telepresence/rpc/v2 v2.34.0-rc.0 // indirect
	github.com/vishvananda/netlink v1.3.1 // indirect
	github.com/vishvananda/netns v0.0.5 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	go.yaml.in/yaml/v2 v2.4.5 // indirect
	golang.org/x/crypto v0.59.0 // indirect
	golang.org/x/exp v0.0.0-20261124000051-d6646c509b6d // indirect
	golang.org/x/net v0.63.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
	golang.org/x/text v0.44.0 // indirect
	golang.org/x/time v0.16.0 // indirect
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
	golang.zx2c4.com/wireguard v0.0.0-20261024220952-b3bc704ef0ee // indirect
	golang.zx2c4.com/wireguard/windows v1.1.1 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20261123191846-1e707ab9fbc2 // indirect
	google.golang.org/grpc v1.85.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gvisor.dev/gvisor v0.0.0-20261124190646-2bf06df5b1d5 // indirect
	k8s.io/api v0.37.2 // indirect
	k8s.io/apimachinery v0.37.2 // indirect
	k8s.io/klog/v2 v2.140.0 // indirect
	k8s.io/kube-openapi v0.0.0-20260721132016-d427ff9ee9ad // indirect
	k8s.io/utils v0.0.0-20261023123148-ca04ba58ecab // indirect
	sigs.k8s.io/json v0.0.0-20260909141634-11ed52e25bc5 // indirect
	sigs.k8s.io/randfill v1.0.0 // indirect
	sigs.k8s.io/structured-merge-diff/v6 v6.4.2 // indirect
	sigs.k8s.io/yaml v1.6.0 // indirect
)

replace github.com/telepresenceio/telepresence/rpc/v2 => ./../../../../rpc

replace github.com/telepresenceio/telepresence/cmd/cobraparser/v2 => ./../../../../cmd/cobraparser

replace github.com/telepresenceio/telepresence/v2 => ./../../../..
