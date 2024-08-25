package intercept

import (
	"fmt"
	"slices"
	"strings"

	"github.com/telepresenceio/telepresence/v2/pkg/shellquote"
)

type EnvironmentSyntax int

const (
	envSyntaxDocker EnvironmentSyntax = iota
	envSyntaxCompose
	envSyntaxSh
	envSyntaxShExport
	envSyntaxCsh
	envSyntaxCshExport
	envSyntaxPS
	envSyntaxPSExport
	envSyntaxCmd
)

var envSyntaxNames = []string{ //nolint:gochecknoglobals // constant
	"docker",
	"compose",
	"sh",
	"sh:export",
	"csh",
	"csh:export",
	"ps",
	"ps:export",
	"cmd",
}

func EnvSyntaxUsage() string {
	return `"docker", "compose", "sh", "csh", "cmd", and "ps"; where "sh", "csh", and "ps" can be suffixed with ":export"`
}

// Set uses a pointer receiver intentionally, even though the internal type is int, because
// it must change the actual receiver value.
func (e *EnvironmentSyntax) Set(n string) error {
	ex := slices.Index(envSyntaxNames, n)
	if ex < 0 {
		return fmt.Errorf("invalid env syntax: %s", n)
	}
	*e = EnvironmentSyntax(ex)
	return nil
}

func (e EnvironmentSyntax) String() string {
	if e >= 0 && e <= envSyntaxCmd {
		return envSyntaxNames[e]
	}
	return "unknown"
}

func (e EnvironmentSyntax) Type() string {
	return "string"
}

// WriteEnv will write the environment variable in a form that will make the target shell parse it correctly and verbatim.
func (e EnvironmentSyntax) WriteEnv(k, v string) (r string, err error) {
	switch e {
	case envSyntaxDocker:
		// Docker does not accept multi-line environments
		if strings.IndexByte(v, '\n') >= 0 {
			return "", fmt.Errorf("docker run/build does not support multi-line environment values: key: %s, value %s", k, v)
		}
		r = fmt.Sprintf("%s=%s", k, v)
	case envSyntaxCompose:
		r = fmt.Sprintf("%s=%s", k, quoteCompose(v))
	case envSyntaxSh:
		r = fmt.Sprintf("%s=%s", k, shellquote.Unix(v))
	case envSyntaxShExport:
		r = fmt.Sprintf("export %s=%s", k, shellquote.Unix(v))
	case envSyntaxCsh:
		r = fmt.Sprintf("set %s=%s", k, shellquote.Unix(v))
	case envSyntaxCshExport:
		r = fmt.Sprintf("setenv %s %s", k, shellquote.Unix(v))
	case envSyntaxPS:
		r = fmt.Sprintf("$Env:%s=%s", k, quotePS(v))
	case envSyntaxPSExport:
		r = fmt.Sprintf("[Environment]::SetEnvironmentVariable(%s, %s, 'User')", quotePS(k), quotePS(v))
	case envSyntaxCmd:
		if strings.IndexByte(v, '\n') >= 0 {
			return "", fmt.Errorf("cmd does not support multi-line environment values: key: %s, value %s", k, v)
		}
		r = fmt.Sprintf("set %s=%s", k, v)
	}
	return r, nil
}

// quotePS will put single quotes around the given value, which effectively removes all special meanings of
// all contained characters, with one exception. Powershell uses pairs of single quotes to represent one single
// quote in a quoted string.
func quotePS(s string) string {
	sb := strings.Builder{}
	sb.WriteByte('\'')
	for _, c := range s {
		if c == '\'' {
			sb.WriteByte('\'')
		}
		sb.WriteRune(c)
	}
	sb.WriteByte('\'')
	return sb.String()
}

// quoteCompose checks if the give string contains characters that have special meaning for
// docker compose. If it does, it will be quoted using either double or single quotes depending
// on whether the string contains newlines, carriage returns, or tabs. Quotes within the value itself will
// be escaped using backslash.
func quoteCompose(s string) string {
	if s == "" {
		return ``
	}
	q := byte('\'')
	if strings.ContainsAny(s, "\n\t\r") {
		q = '"'
	} else if !shellquote.UnixEscape.MatchString(s) {
		return s
	}

	sb := strings.Builder{}
	sb.WriteByte(q)
	for _, c := range s {
		switch c {
		case rune(q):
			sb.WriteByte('\\')
			sb.WriteRune(c)
		case '\n':
			sb.WriteByte('\\')
			sb.WriteByte('n')
		case '\t':
			sb.WriteByte('\\')
			sb.WriteByte('t')
		case '\r':
			sb.WriteByte('\\')
			sb.WriteByte('r')
		default:
			sb.WriteRune(c)
		}
	}
	sb.WriteByte(q)
	return sb.String()
}
