package generate

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/opts"
	"github.com/spf13/pflag"

	"github.com/telepresenceio/telepresence/cmd/cobraparser/v2/types"
)

// FlagSet creates a FlagSet based on the given CommandInfo.
//
//nolint:gocyclo // there are a lot of cases here
func FlagSet(setName string, ci *types.CommandInfo) *pflag.FlagSet {
	flags := pflag.NewFlagSet(setName, pflag.ContinueOnError)
	for _, flag := range ci.Flags {
		switch {
		case flag.Type == "bool":
			val := false
			if flag.Default != "" {
				val, _ = strconv.ParseBool(flag.Default)
			}
			flags.Bool(flag.Name, val, flag.Description)
		case flag.Type == "boolSlice":
			flags.BoolSlice(flag.Name, nil, flag.Description)
		case flag.Type == "duration":
			val := time.Duration(0)
			if flag.Default != "" {
				val, _ = time.ParseDuration(flag.Default)
			}
			flags.Duration(flag.Name, val, flag.Description)
		case flag.Type == "durationSlice":
			flags.DurationSlice(flag.Name, nil, flag.Description)
		case flag.Type == "string":
			flags.String(flag.Name, flag.Default, flag.Description)
		case flag.Type == "stringArray":
			var val []string
			if flag.Default != "" {
				val = strings.Split(flag.Default, ",")
			}
			flags.StringArray(flag.Name, val, flag.Description)
		case flag.Type == "stringSlice":
			flags.StringSlice(flag.Name, nil, flag.Description)
		case strings.HasPrefix(flag.Type, "int"):
			val := int64(0)
			if flag.Default != "" {
				val, _ = strconv.ParseInt(flag.Default, 10, 64)
			}
			switch flag.Type {
			case "int":
				flags.Int(flag.Name, int(val), flag.Description)
			case "int8":
				flags.Int8(flag.Name, int8(val), flag.Description)
			case "int16":
				flags.Int16(flag.Name, int16(val), flag.Description)
			case "int32":
				flags.Int32(flag.Name, int32(val), flag.Description)
			case "int64":
				flags.Int64(flag.Name, val, flag.Description)
			case "intSlice":
				flags.IntSlice(flag.Name, nil, flag.Description)
			case "int8Slice":
				flags.IntSlice(flag.Name, nil, flag.Description)
			case "int16Slice":
				flags.IntSlice(flag.Name, nil, flag.Description)
			case "int32Slice":
				flags.Int32Slice(flag.Name, nil, flag.Description)
			case "int64Slice":
				flags.Int64Slice(flag.Name, nil, flag.Description)
			}
		case strings.HasPrefix(flag.Type, "uint"):
			val := uint64(0)
			if flag.Default != "" {
				val, _ = strconv.ParseUint(flag.Default, 10, 64)
			}
			switch flag.Type {
			case "uint":
				flags.Uint(flag.Name, uint(val), flag.Description)
			case "uint8":
				flags.Uint8(flag.Name, uint8(val), flag.Description)
			case "uint16":
				flags.Uint16(flag.Name, uint16(val), flag.Description)
			case "uint32":
				flags.Uint32(flag.Name, uint32(val), flag.Description)
			case "uint64":
				flags.Uint64(flag.Name, val, flag.Description)
			case "uintSlice":
				flags.UintSlice(flag.Name, nil, flag.Description)
			}
		case strings.HasPrefix(flag.Type, "float"):
			val := float64(0)
			if flag.Default != "" {
				val, _ = strconv.ParseFloat(flag.Default, 64)
			}
			switch flag.Type {
			case "float32":
				flags.Float32(flag.Name, float32(val), flag.Description)
			case "float64":
				flags.Float64(flag.Name, val, flag.Description)
			case "float32Slice":
				flags.Float32Slice(flag.Name, nil, flag.Description)
			case "float564Slice":
				flags.Float64Slice(flag.Name, nil, flag.Description)
			}
		case flag.Type == "bytes":
			flags.Var(new(opts.MemBytes), flag.Name, flag.Description)
		case flag.Type == "filter":
			flags.String(flag.Name, flag.Default, flag.Description)
		case flag.Type == "list":
			flags.Var(opts.NewListOptsRef(new([]string), nil), flag.Name, flag.Description)
		case flag.Type == "scale":
			flags.Uint32(flag.Name, 0, flag.Description)
		default:
			panic(fmt.Sprintf("unknown flag type %s in flagset %s", flag.Type, setName))
		}
		if flag.Shorthand != "" {
			flags.Lookup(flag.Name).Shorthand = flag.Shorthand
		}
	}
	return flags
}
