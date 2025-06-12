package driver

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
)

type options struct {
	// host IP on the default bridge network for the Telepresence daemon
	host netip.Addr

	// port where the Telepresence daemon starts its teleroute server
	port uint16
}

type errRequiredOption string

func (e errRequiredOption) Error() string {
	return fmt.Sprintf("option %q is required", e)
}

func (o *options) parse(gos map[string]any) error {
	unableToParse := func(opt string, err error) error {
		return fmt.Errorf("unable to parse %q: %w", opt, err)
	}
	for key, anyVal := range gos {
		val, ok := anyVal.(string)
		if !ok {
			return unableToParse(key, fmt.Errorf("invalid value type %T", anyVal))
		}
		var err error
		switch key {
		case "host":
			o.host, err = netip.ParseAddr(val)
			if err != nil {
				return unableToParse(key, err)
			}
		case "port":
			var port uint64
			if port, err = strconv.ParseUint(val, 10, 16); err == nil {
				if port == 0 {
					return unableToParse(key, errors.New("cannot be 0"))
				}
				o.port = uint16(port)
			}
		default:
			return fmt.Errorf("illegal option %q", key)
		}
	}
	if !o.host.IsValid() {
		return errRequiredOption("host")
	}
	if o.port == 0 {
		return errRequiredOption("port")
	}
	return nil
}
