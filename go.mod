module github.com/datawire/telepresence2

go 1.15

require (
	git.lukeshu.com/go/libsystemd v0.5.3
	github.com/datawire/ambassador v1.8.1
	github.com/datawire/pf v0.0.0-20180510150411-31a823f9495a
	github.com/dgrijalva/jwt-go v3.2.0+incompatible
	github.com/golang/protobuf v1.4.3
	github.com/google/uuid v1.1.2
	github.com/gookit/color v1.3.1
	github.com/miekg/dns v1.1.34
	github.com/pkg/browser v0.0.0-20180916011732-0a3d74bf9ce4
	github.com/pkg/errors v0.9.1
	github.com/sirupsen/logrus v1.7.0
	github.com/spf13/cobra v1.1.1
	github.com/spf13/pflag v1.0.5
	github.com/stretchr/testify v1.6.1
	golang.org/x/crypto v0.0.0-20201016220609-9e8e0b390897
	golang.org/x/net v0.0.0-20201016165138-7b1cca2348c0
	golang.org/x/sync v0.0.0-20190911185100-cd5d95a43a6e
	google.golang.org/grpc v1.33.0
	google.golang.org/grpc/cmd/protoc-gen-go-grpc v1.0.1 // indirect
	google.golang.org/protobuf v1.25.0
	gopkg.in/natefinch/lumberjack.v2 v2.0.0
	k8s.io/apimachinery v0.18.4
	k8s.io/client-go v0.18.4
)

replace github.com/Azure/go-autorest v10.8.1+incompatible => github.com/Azure/go-autorest v13.3.2+incompatible

// This is a workaround for a Go backwards incompatibility problem introduced in 1.15.2 which breaks
// github/docker/docker/pkg/term on darwin because unix.SYS_IOCTL no longer exists. Having ambassador use
// a more recent version of k8s.io/kubectl/pkg/util/term in (this is where the dependency is introduced) will
// likely also solve the problem. The github.com/docker/docker is deprecated.
replace github.com/docker/docker v1.4.2-0.20200203170920-46ec8731fbce => github.com/moby/moby v1.13.1
