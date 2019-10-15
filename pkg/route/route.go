package route

import (
	"strings"
)

type Table struct {
	Name   string  `json:"name"`
	Routes []Route `json:"routes"`
}

func (t *Table) Add(route Route) {
	t.Routes = append(t.Routes, route)
}

type Route struct {
	Name   string `json:"name,omitempty"`
	Ip     string `json:"ip"`
	Proto  string `json:"proto"`
	Port   string `json:"port,omitempty"`
	Target string `json:"target"`
	Action string `json:"action,omitempty"`
}

func (r Route) Domain() string {
	return strings.ToLower(r.Name + ".")
}
