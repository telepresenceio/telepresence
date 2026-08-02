package compose

// Extension is a service's x-tele block: Connect, Proxy, Ingest, Intercept,
// Replace, or Wiretap (pkg/client/cli/docker/compose/extension.go's
// serviceExtension family, selected there by the "type" field
// types.ParseAttachmentType parses).
type Extension interface {
	// xTele returns the "type" discriminator plus this extension's own
	// fields, merged into the owning service's x-tele YAML mapping.
	xTele() map[string]any
}

// Connect gives the service access to the cluster's DNS and routing
// (docs/reference/compose.md#connect); every other extension carries the
// same Connection field.
type Connect struct {
	// Connection names a connection declared in the top-level x-tele
	// extension; required only when more than one connection is declared.
	Connection string
}

func (c Connect) xTele() map[string]any {
	m := map[string]any{"type": "connect"}
	setStr(m, "connection", c.Connection)
	return m
}

// Proxy replaces the service with a proxy that redirects all traffic to a
// cluster service (docs/reference/compose.md#proxy).
type Proxy struct {
	Connection string
	// Name is the proxied remote service's name; empty defaults to the
	// compose service's own name.
	Name string
	// Ports maps <local port>:<remote service port>; empty routes every
	// port as-is.
	Ports []string
}

func (p Proxy) xTele() map[string]any {
	m := map[string]any{"type": "proxy"}
	setStr(m, "connection", p.Connection)
	setStr(m, "name", p.Name)
	setStrs(m, "ports", p.Ports)
	return m
}

// Ingest shares the environment and volumes of a remote container
// (docs/reference/compose.md#ingest).
type Ingest struct {
	Connection string
	// Name is the remote workload's name; empty defaults to the compose
	// service's own name.
	Name      string
	Container string
	// ToPod forwards additional local ports to the remote pod's localhost.
	ToPod []string
}

func (i Ingest) xTele() map[string]any {
	m := map[string]any{"type": "ingest"}
	setStr(m, "connection", i.Connection)
	setStr(m, "name", i.Name)
	setStr(m, "container", i.Container)
	setStrs(m, "toPod", i.ToPod)
	return m
}

// Intercept receives cluster traffic destined for a workload, sharing its
// environment and volumes (docs/reference/compose.md#intercept).
type Intercept struct {
	Connection string
	// Name is the intercept attachment's name; empty defaults to the
	// compose service's own name.
	Name     string
	Workload string
	Service  string
	// Ports maps <local port>:<service port> to intercept.
	Ports            []string
	HTTPFilters      map[string]string
	Metadata         map[string]string
	HTTPPaths        []string
	HTTPPathPrefixes []string
	HTTPPathRegexps  []string
	ToPod            []string
}

func (i Intercept) xTele() map[string]any {
	m := map[string]any{"type": "intercept"}
	setStr(m, "connection", i.Connection)
	setStr(m, "name", i.Name)
	setStr(m, "workload", i.Workload)
	setStr(m, "service", i.Service)
	setStrs(m, "ports", i.Ports)
	setMap(m, "httpFilters", i.HTTPFilters)
	setMap(m, "metadata", i.Metadata)
	setStrs(m, "httpPaths", i.HTTPPaths)
	setStrs(m, "httpPathPrefixes", i.HTTPPathPrefixes)
	setStrs(m, "httpPathRegexps", i.HTTPPathRegexps)
	setStrs(m, "toPod", i.ToPod)
	return m
}

// Replace substitutes a container in a remote workload with the compose
// service, sharing its environment and volumes
// (docs/reference/compose.md#replace).
type Replace struct {
	Connection string
	// Name is the remote workload's name; empty defaults to the compose
	// service's own name.
	Name      string
	Container string
	// Ports maps <local port>:<container port> to replace.
	Ports []string
	ToPod []string
}

func (r Replace) xTele() map[string]any {
	m := map[string]any{"type": "replace"}
	setStr(m, "connection", r.Connection)
	setStr(m, "name", r.Name)
	setStr(m, "container", r.Container)
	setStrs(m, "ports", r.Ports)
	setStrs(m, "toPod", r.ToPod)
	return m
}

// Wiretap receives copies of cluster traffic destined for a workload
// without disturbing the original flow (docs/reference/compose.md#wiretap).
// Unlike Intercept/Replace/Ingest, the product's wiretapExtension type
// (pkg/client/cli/docker/compose/extension.go) declares no toPod field.
type Wiretap struct {
	Connection string
	// Name is the wiretapped workload's name; empty defaults to the
	// compose service's own name.
	Name             string
	Service          string
	Ports            []string
	HTTPFilters      map[string]string
	Metadata         map[string]string
	HTTPPaths        []string
	HTTPPathPrefixes []string
	HTTPPathRegexps  []string
}

func (w Wiretap) xTele() map[string]any {
	m := map[string]any{"type": "wiretap"}
	setStr(m, "connection", w.Connection)
	setStr(m, "name", w.Name)
	setStr(m, "service", w.Service)
	setStrs(m, "ports", w.Ports)
	setMap(m, "httpFilters", w.HTTPFilters)
	setMap(m, "metadata", w.Metadata)
	setStrs(m, "httpPaths", w.HTTPPaths)
	setStrs(m, "httpPathPrefixes", w.HTTPPathPrefixes)
	setStrs(m, "httpPathRegexps", w.HTTPPathRegexps)
	return m
}

// setStr adds k:v to m unless v is the empty string.
func setStr(m map[string]any, k, v string) {
	if v != "" {
		m[k] = v
	}
}

// setStrs adds k:v to m unless v is empty.
func setStrs(m map[string]any, k string, v []string) {
	if len(v) > 0 {
		m[k] = v
	}
}

// setMap adds k:v to m unless v is empty.
func setMap(m map[string]any, k string, v map[string]string) {
	if len(v) > 0 {
		m[k] = v
	}
}
