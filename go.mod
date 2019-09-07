module github.com/datawire/teleproxy

go 1.13

require (
	cloud.google.com/go v0.34.0 // indirect
	git.lukeshu.com/go/libsystemd v0.0.0-20181219160046-e05011ef37a6
	github.com/BurntSushi/toml v0.3.1 // indirect
	github.com/Masterminds/semver v1.4.2 // indirect
	github.com/Masterminds/sprig v2.17.1+incompatible
	github.com/aokoli/goutils v1.1.0 // indirect
	github.com/bmizerany/assert v0.0.0-20160611221934-b7ed37b82869
	github.com/datawire/libk8s v0.0.0-20190907232252-778ff575ee65
	github.com/datawire/pf v0.0.0-20180510150411-31a823f9495a
	github.com/dgrijalva/jwt-go v3.2.0+incompatible // indirect
	github.com/ecodia/golang-awaitility v0.0.0-20180710094957-fb55e59708c7
	github.com/gogo/protobuf v1.1.1 // indirect
	github.com/google/shlex v0.0.0-20181106134648-c34317bd91bf
	github.com/google/uuid v1.1.1 // indirect
	github.com/googleapis/gnostic v0.2.0 // indirect
	github.com/gregjones/httpcache v0.0.0-20180305231024-9cad4c3443a7 // indirect
	github.com/hashicorp/consul/api v1.1.0
	github.com/hashicorp/go-msgpack v0.5.4 // indirect
	github.com/hashicorp/go-sockaddr v1.0.2 // indirect
	github.com/huandu/xstrings v1.2.0 // indirect
	github.com/imdario/mergo v0.3.6 // indirect
	github.com/json-iterator/go v1.1.5 // indirect
	github.com/kballard/go-shellquote v0.0.0-20180428030007-95032a82bc51
	github.com/kr/pretty v0.1.0 // indirect
	github.com/miekg/dns v1.1.6
	github.com/mitchellh/mapstructure v1.1.2
	github.com/pascaldekloe/goe v0.1.0 // indirect
	github.com/pkg/errors v0.8.1
	github.com/sirupsen/logrus v1.4.0
	github.com/spf13/cobra v0.0.3
	github.com/spf13/pflag v1.0.3
	github.com/stretchr/testify v1.3.0
	golang.org/x/crypto v0.0.0-20190605123033-f99c8df09eb5
	golang.org/x/net v0.0.0-20190812203447-cdfb69ac37fc
	golang.org/x/oauth2 v0.0.0-20181203162652-d668ce993890 // indirect
	golang.org/x/time v0.0.0-20180412165947-fbb02b2291d2 // indirect
	gopkg.in/check.v1 v1.0.0-20180628173108-788fd7840127 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.0.0
	gopkg.in/yaml.v2 v2.2.2
	k8s.io/apimachinery v0.0.0-20190816201409-1714e684133b
	k8s.io/cli-runtime v0.0.0-20190816224449-59d96aa1e208
	k8s.io/client-go v11.0.1-0.20190816222228-6d55c1b1f1ca+incompatible
	sigs.k8s.io/yaml v1.1.0
)

// Fix invalid pseudo-version that Go 1.13 complains about.
replace github.com/census-instrumentation/opencensus-proto v0.1.0-0.20181214143942-ba49f56771b8 => github.com/census-instrumentation/opencensus-proto v0.0.3-0.20181214143942-ba49f56771b8
