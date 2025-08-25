package types

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

type EngagementType int

var engagementTypes = map[string]EngagementType{ //nolint:gochecknoglobals // constant names
	"connect":   EngagementTypeConnect,
	"ingest":    EngagementTypeIngest,
	"wiretap":   EngagementTypeWiretap,
	"intercept": EngagementTypeIntercept,
	"replace":   EngagementTypeReplace,
	"proxy":     EngagementTypeProxy,
}

const (
	EngagementTypeConnect = EngagementType(iota)
	EngagementTypeIngest
	EngagementTypeWiretap
	EngagementTypeIntercept
	EngagementTypeReplace
	EngagementTypeProxy
)

var egStrings = [6][5]string{ //nolint:gochecknoglobals // constant names
	{
		"connect",
		"Connecting",
		"Connected",
		"Disconnecting",
		"Disconnected",
	},
	{
		"ingest",
		"Ingesting",
		"Ingested",
		"Leaving ingest",
		"Left ingest",
	},
	{
		"wiretap",
		"Wiretapping",
		"Wiretapped",
		"Removing wiretap",
		"Removed wiretap",
	},
	{
		"intercept",
		"Intercepting",
		"Intercepted",
		"Leaving intercept",
		"Left intercept",
	},
	{
		"replace",
		"Replacing",
		"Replaced",
		"Restoring",
		"Restored",
	},
	{
		"proxy",
		"Proxying",
		"Proxied",
		"Removing proxy",
		"Removed proxy",
	},
}

const invalidType = "invalid engagement type %s"

func ParseEngagementType(s string) (EngagementType, error) {
	if e, ok := engagementTypes[strings.ToLower(s)]; ok {
		return e, nil
	}
	return 0, fmt.Errorf(invalidType, s)
}

func EngagementTypeFromSpec(spec *manager.InterceptSpec) EngagementType {
	switch {
	case spec.Wiretap:
		return EngagementTypeWiretap
	case spec.NoDefaultPort:
		return EngagementTypeReplace
	default:
		return EngagementTypeIntercept
	}
}

func (e EngagementType) strings() [5]string {
	if e >= 0 && e < 6 {
		return egStrings[e]
	}
	en := fmt.Sprintf(invalidType, strconv.Itoa(int(e)))
	return [5]string{en, en, en, en, en}
}

func (e EngagementType) String() string {
	return e.strings()[0]
}

func (e EngagementType) Working() string {
	return e.strings()[1]
}

func (e EngagementType) WorkDone() string {
	return e.strings()[2]
}

func (e EngagementType) Leaving() string {
	return e.strings()[3]
}

func (e EngagementType) Left() string {
	return e.strings()[4]
}

func (e EngagementType) MarshalJSONTo(out *jsontext.Encoder) error {
	return json.MarshalEncode(out, e.String())
}

//goland:noinspection GoMixedReceiverTypes
func (e *EngagementType) UnmarshalJSONFrom(in *jsontext.Decoder) error {
	var s string
	err := json.UnmarshalDecode(in, &s)
	if err == nil {
		*e, err = ParseEngagementType(s)
	}
	return err
}
