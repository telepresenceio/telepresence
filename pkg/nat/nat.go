package nat

import (
	"fmt"
	"sort"
	"strings"
)

type commonTranslator struct {
	Name     string
	Mappings map[Address]string
}

type Address struct {
	Proto string
	Ip    string
	Port  string
}

type Entry struct {
	Destination Address
	Port        string
}

func (e *Entry) String() string {
	return fmt.Sprintf("%s:%s->%s", e.Destination.Proto, e.Destination.Ip, e.Port)
}

func (t *Translator) sorted() []Entry {
	entries := make([]Entry, len(t.Mappings))

	index := 0
	for k, v := range t.Mappings {
		entries[index] = Entry{k, v}
		index += 1
	}

	sort.Slice(entries, func(i, j int) bool {
		return strings.Compare(entries[i].String(), entries[j].String()) < 0
	})

	return entries
}

func NewTranslator(name string) *Translator {
	var t Translator
	t.Name = name
	t.Mappings = make(map[Address]string)
	return &t
}
