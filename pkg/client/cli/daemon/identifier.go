package daemon

import (
	"fmt"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

type Identifier struct {
	KubeContext string
	Namespace   string
}

func NewIdentifier(contextName, namespace string) *Identifier {
	return &Identifier{KubeContext: contextName, Namespace: namespace}
}

func (id *Identifier) String() string {
	return SafeContainerName(id.KubeContext + "-" + id.Namespace)
}

func (id *Identifier) DaemonInfoFileName(port int) string {
	return fmt.Sprintf("%s-%d.json", id.String(), port)
}

func (id *Identifier) ContainerName() string {
	return "tp-" + id.String()
}

// IdentifierFromFlags returns a unique name created from the name of the current context
// and the active namespace denoted by the given flagMap.
func IdentifierFromFlags(flagMap map[string]string) (*Identifier, error) {
	cld, err := client.ConfigLoader(flagMap)
	if err != nil {
		return nil, err
	}
	ns, _, err := cld.Namespace()
	if err != nil {
		return nil, err
	}

	config, err := cld.RawConfig()
	if err != nil {
		return nil, err
	}
	cc := flagMap["context"]
	if cc == "" {
		cc = config.CurrentContext
	}
	return NewIdentifier(cc, ns), nil
}
