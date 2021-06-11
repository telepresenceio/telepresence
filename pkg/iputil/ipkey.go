package iputil

import (
	"encoding/json"
	"fmt"
	"net"
)

// IPKey is an immutable cast of a net.IP suitable to be used as a map key. It must be created using IPKey(ip)
type IPKey string

func (k IPKey) IP() net.IP {
	return net.IP(k)
}

// String returns the human readable string form of the IP (as opposed to the binary junk displayed when using it directly).
func (k IPKey) String() string {
	return net.IP(k).String()
}

func (k IPKey) MarshalJSON() ([]byte, error) {
	return json.Marshal(k.String())
}

func (k *IPKey) UnmarshalJSON(data []byte) error {
	var str string
	if err := json.Unmarshal(data, &str); err != nil {
		return err
	}
	ip := Parse(str)
	if ip == nil {
		return fmt.Errorf("invalid IP %q", str)
	}
	*k = IPKey(ip)
	return nil
}
