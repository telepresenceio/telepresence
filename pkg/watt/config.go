package watt

import (
	"github.com/datawire/teleproxy/pkg/supervisor"
)

type Config struct {
	Watches []WatchSpec
}

type WatchSpec struct {
	Type string
	Spec interface{}
}

type KubernetesWatchSpec struct {
	Namespace string
	Kinds     []string
}

type ConsulWatchSpec struct {
	Service    string
	Datacenter string
}

//func ConfigFromFile(configFile string) (*Config, error) {
//	var res *Config
//	var err error
//
//	jsonFile, err := os.Open(configFile)
//	if err != nil {
//		return nil, err
//	}
//
//	defer func() {
//		if err := jsonFile.Close(); err != nil {
//			fmt.Printf("error: closing config file failed")
//		}
//	}()
//
//	jsonBytes, err := ioutil.ReadAll(jsonFile)
//	if err != nil {
//		return res, err
//	}
//
//	var msg json.RawMessage
//
//
//	err = json.Unmarshal(jsonBytes, res)
//	return res, err
//}

type WatcherMaker interface {
	ID() string
	Make() (func(p *supervisor.Process) error, error)
}

type KubernetesResourceWatchMaker struct {
	Kind    string `json:""`
	Version string `json:""`
}
