module github.com/datawire/telepresence2/v2

go 1.15

require (
	github.com/blang/semver v3.5.0+incompatible
	github.com/datawire/ambassador v1.11.1
	github.com/datawire/dlib v1.2.0
	github.com/datawire/pf v0.0.0-20180510150411-31a823f9495a
	github.com/datawire/telepresence2/rpc/v2 v2.1.0-rc.0
	github.com/docker/docker v1.4.2-0.20200203170920-46ec8731fbce
	github.com/godbus/dbus/v5 v5.0.4-0.20201218172701-b3768b321399
	github.com/golang/protobuf v1.4.3
	github.com/google/go-cmp v0.5.0
	github.com/google/uuid v1.1.2
	github.com/miekg/dns v1.1.35
	github.com/pkg/browser v0.0.0-20180916011732-0a3d74bf9ce4
	github.com/pkg/errors v0.9.1
	github.com/sethvargo/go-envconfig v0.3.2
	github.com/sirupsen/logrus v1.7.0
	github.com/spf13/cobra v1.1.1
	github.com/spf13/pflag v1.0.5
	github.com/stretchr/testify v1.6.1
	golang.org/x/crypto v0.0.0-20201016220609-9e8e0b390897
	golang.org/x/net v0.0.0-20210119194325-5f4716e94777
	golang.org/x/oauth2 v0.0.0-20200107190931-bf48bf16ab8d
	golang.org/x/sync v0.0.0-20201020160332-67f06af15bc9
	golang.org/x/sys v0.0.0-20201119102817-f84b799fce68
	google.golang.org/appengine v1.6.7 // indirect
	google.golang.org/genproto v0.0.0-20200806141610-86f49bd18e98 // indirect
	google.golang.org/grpc v1.34.0
	google.golang.org/protobuf v1.25.0
	gopkg.in/yaml.v2 v2.3.0
	gotest.tools v2.2.0+incompatible
	k8s.io/api v0.18.8
	k8s.io/apiextensions-apiserver v0.18.8 // indirect
	k8s.io/apimachinery v0.18.8
	k8s.io/client-go v0.18.8
	k8s.io/kubectl v0.18.8 // indirect
	sigs.k8s.io/yaml v1.2.0
)

replace github.com/Azure/go-autorest v10.8.1+incompatible => github.com/Azure/go-autorest v13.3.2+incompatible

// Pin down golang.org/x/sys to a version that contains unix.SYS_IOCTL on darwin
replace golang.org/x/sys => golang.org/x/sys v0.0.0-20200826173525-f9321e4c35a6

replace github.com/datawire/telepresence2/rpc/v2 => ./rpc
