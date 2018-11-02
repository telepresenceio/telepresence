package route

import (
	"strings"
)

type Table struct {
	Name string
	Routes []Route
}

func (t *Table)  Add(route Route) {
	t.Routes = append(t.Routes, route)
}

type Route struct {
	Name, Ip, Proto, Target, Action string
}

func (r Route) Domain() string {
	return strings.ToLower(r.Name + ".")
}
