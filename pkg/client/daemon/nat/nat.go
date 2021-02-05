package nat

import (
	"fmt"
)

type commonTranslator struct {
	Name     string
	Mappings map[Address]string
}

type Address struct {
	Proto string
	IP    string
	Port  string
}

type Entry struct {
	Destination Address
	Port        string
}

func (e *Entry) String() string {
	return fmt.Sprintf("%s:%s->%s", e.Destination.Proto, e.Destination.IP, e.Port)
}

func NewTranslator(name string) *Translator {
	var t Translator
	t.Name = name
	t.Mappings = make(map[Address]string)
	return &t
}
