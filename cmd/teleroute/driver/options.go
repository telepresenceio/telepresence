package driver

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
)

type options struct {
	host     netip.Addr
	port     uint16
	pid      int
	ifPrefix string
}

func (o *options) parse(gos map[string]any) error {
	unableToParse := func(opt string, err error) error {
		return fmt.Errorf("unable to parse %q: %w", opt, err)
	}
	o.ifPrefix = "tel"
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
		case "pid":
			o.pid, err = strconv.Atoi(val)
			if err != nil {
				return unableToParse(key, err)
			}
		case "ifPrefix":
			o.ifPrefix = val
		default:
			return fmt.Errorf("illegal option %q", key)
		}
	}
	return nil
}
