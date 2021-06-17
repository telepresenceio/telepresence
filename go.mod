module github.com/telepresenceio/telepresence/v2

go 1.15

require (
	github.com/blang/semver v3.5.1+incompatible
	github.com/datawire/ambassador v1.13.7-0.20210527054604-663dfb393e59
	github.com/datawire/dlib v1.2.1
	github.com/docker/docker v1.4.2-0.20200203170920-46ec8731fbce
	github.com/godbus/dbus/v5 v5.0.4-0.20201218172701-b3768b321399
	github.com/google/go-cmp v0.5.5
	github.com/google/uuid v1.1.2
	github.com/kballard/go-shellquote v0.0.0-20180428030007-95032a82bc51
	github.com/miekg/dns v1.1.35
	github.com/pkg/browser v0.0.0-20180916011732-0a3d74bf9ce4
	github.com/pkg/errors v0.9.1
	github.com/sethvargo/go-envconfig v0.3.2
	github.com/sirupsen/logrus v1.7.0
	github.com/spf13/cobra v1.1.1
	github.com/spf13/pflag v1.0.5
	github.com/stretchr/testify v1.7.0
	github.com/telepresenceio/telepresence/rpc/v2 v2.3.1
	golang.org/x/net v0.0.0-20210226172049-e18ecbb05110
	golang.org/x/oauth2 v0.0.0-20200107190931-bf48bf16ab8d
	golang.org/x/sys v0.0.0-20210124154548-22da62e12c0c
	golang.org/x/term v0.0.0-20201126162022-7de9c90e9dd1
	google.golang.org/grpc v1.34.0
	google.golang.org/protobuf v1.25.0
	gopkg.in/yaml.v3 v3.0.0-20200615113413-eeeca48fe776
	gotest.tools v2.2.0+incompatible
	k8s.io/api v0.20.2
	k8s.io/apimachinery v0.20.2
	k8s.io/client-go v0.20.2
	sigs.k8s.io/yaml v1.2.0
)

// We need to inherit this from github.com/datawire/ambassador
replace (
	github.com/Azure/go-autorest v10.8.1+incompatible => github.com/Azure/go-autorest v13.3.2+incompatible
	github.com/docker/distribution => github.com/docker/distribution v0.0.0-20191216044856-a8371794149d
	github.com/docker/docker => github.com/moby/moby v17.12.0-ce-rc1.0.20200618181300-9dc6525e6118+incompatible
)

replace github.com/telepresenceio/telepresence/rpc/v2 => ./rpc
