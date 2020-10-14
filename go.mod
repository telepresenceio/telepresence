module github.com/datawire/telepresence

go 1.15

require (
	git.lukeshu.com/go/libsystemd v0.5.3
	github.com/datawire/ambassador v1.8.0
	github.com/datawire/pf v0.0.0-20180510150411-31a823f9495a
	github.com/dgrijalva/jwt-go v3.2.0+incompatible
	github.com/golang/protobuf v1.4.2
	github.com/google/uuid v1.1.2
	github.com/gookit/color v1.3.1
	github.com/miekg/dns v1.1.33
	github.com/pkg/browser v0.0.0-20180916011732-0a3d74bf9ce4
	github.com/pkg/errors v0.9.1
	github.com/sirupsen/logrus v1.7.0
	github.com/spf13/cobra v1.0.0
	github.com/stretchr/testify v1.6.1
	golang.org/x/crypto v0.0.0-20200622213623-75b288015ac9
	golang.org/x/net v0.0.0-20200707034311-ab3426394381
	google.golang.org/grpc v1.33.0
	google.golang.org/protobuf v1.25.0
	gopkg.in/natefinch/lumberjack.v2 v2.0.0
	k8s.io/apiextensions-apiserver v0.18.8 // indirect
	k8s.io/apimachinery v0.18.8
	k8s.io/client-go v0.18.8
	k8s.io/kubectl v0.18.8 // indirect
)

replace github.com/Azure/go-autorest v10.8.1+incompatible => github.com/Azure/go-autorest v13.3.2+incompatible
