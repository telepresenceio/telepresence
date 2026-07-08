package types

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"strconv"
	"strings"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

type AttachmentType int

var attachmentTypes = map[string]AttachmentType{ //nolint:gochecknoglobals // constant names
	"connect":   AttachmentTypeConnect,
	"ingest":    AttachmentTypeIngest,
	"wiretap":   AttachmentTypeWiretap,
	"intercept": AttachmentTypeIntercept,
	"replace":   AttachmentTypeReplace,
	"proxy":     AttachmentTypeProxy,
}

const (
	AttachmentTypeConnect = AttachmentType(iota)
	AttachmentTypeIngest
	AttachmentTypeWiretap
	AttachmentTypeIntercept
	AttachmentTypeReplace
	AttachmentTypeProxy
)

var atStrings = [6][5]string{ //nolint:gochecknoglobals // constant names
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

const invalidType = "invalid attachment type %s"

func ParseAttachmentType(s string) (AttachmentType, error) {
	if e, ok := attachmentTypes[strings.ToLower(s)]; ok {
		return e, nil
	}
	return 0, fmt.Errorf(invalidType, s)
}

func AttachmentTypeFromSpec(spec *manager.InterceptSpec) AttachmentType {
	switch {
	case spec.Wiretap:
		return AttachmentTypeWiretap
	case spec.NoDefaultPort:
		return AttachmentTypeReplace
	default:
		return AttachmentTypeIntercept
	}
}

func (e AttachmentType) strings() [5]string {
	if e >= 0 && e < 6 {
		return atStrings[e]
	}
	en := fmt.Sprintf(invalidType, strconv.Itoa(int(e)))
	return [5]string{en, en, en, en, en}
}

func (e AttachmentType) String() string {
	return e.strings()[0]
}

func (e AttachmentType) Working() string {
	return e.strings()[1]
}

func (e AttachmentType) WorkDone() string {
	return e.strings()[2]
}

func (e AttachmentType) Leaving() string {
	return e.strings()[3]
}

func (e AttachmentType) Left() string {
	return e.strings()[4]
}

func (e AttachmentType) MarshalJSONTo(out *jsontext.Encoder) error {
	return json.MarshalEncode(out, e.String())
}

//goland:noinspection GoMixedReceiverTypes
func (e *AttachmentType) UnmarshalJSONFrom(in *jsontext.Decoder) error {
	var s string
	err := json.UnmarshalDecode(in, &s)
	if err == nil {
		*e, err = ParseAttachmentType(s)
	}
	return err
}
