package env

import (
	"bufio"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/shellquote"
)

type Syntax int

const (
	SyntaxDocker Syntax = iota
	SyntaxCompose
	SyntaxSh
	SyntaxShExport
	SyntaxCsh
	SyntaxCshExport
	SyntaxPS
	SyntaxPSExport
	SyntaxCmd
	SyntaxJSON
)

var syntaxNames = []string{ //nolint:gochecknoglobals // constant
	"docker",
	"compose",
	"sh",
	"sh:export",
	"csh",
	"csh:export",
	"ps",
	"ps:export",
	"cmd",
	"json",
}

func SyntaxUsage() string {
	return `"docker", "compose", "sh", "csh", "cmd", "json", and "ps"; where "sh", "csh", and "ps" can be suffixed with ":export"`
}

// Set uses a pointer receiver intentionally, even though the internal type is int, because
// it must change the actual receiver value.
//
//goland:noinspection GoMixedReceiverTypes
func (e *Syntax) Set(n string) error {
	ex := slices.Index(syntaxNames, n)
	if ex < 0 {
		return fmt.Errorf("invalid env syntax: %s", n)
	}
	*e = Syntax(ex)
	return nil
}

//goland:noinspection GoMixedReceiverTypes
func (e Syntax) String() string {
	if e >= 0 && e <= SyntaxCmd {
		return syntaxNames[e]
	}
	return "unknown"
}

//goland:noinspection GoMixedReceiverTypes
func (e Syntax) Type() string {
	return "string"
}

//goland:noinspection GoMixedReceiverTypes
func (e Syntax) writeFile(fileName string, env map[string]string) error {
	if fileName == "-" {
		return e.write(os.Stdout, env)
	}
	file, err := os.Create(fileName)
	if err != nil {
		return errcat.NoDaemonLogs.Errorf(err, "failed to create environment file %q", fileName)
	}
	return e.WriteToFileAndClose(file, env)
}

//goland:noinspection GoMixedReceiverTypes
func (e Syntax) WriteToFileAndClose(file *os.File, env map[string]string) error {
	err := e.write(file, env)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

//goland:noinspection GoMixedReceiverTypes
func (e Syntax) write(out io.Writer, env map[string]string) error {
	if e == SyntaxJSON {
		data, err := json.Marshal(env, jsontext.WithIndent("  "))
		if err != nil {
			// Creating JSON from a map[string]string should never fail
			panic(err)
		}
		_, err = out.Write(append(data, '\n'))
		return err
	}

	w := bufio.NewWriter(out)

	keys := make([]string, len(env))
	i := 0
	for k := range env {
		keys[i] = k
		i++
	}
	sort.Strings(keys)

	for _, k := range keys {
		r, err := e.WriteEntry(k, env[k])
		if err != nil {
			return err
		}
		if _, err = fmt.Fprintln(w, r); err != nil {
			return err
		}
	}
	return w.Flush()
}

// WriteEntry will write the environment variable in a form that will make the target shell parse it correctly and verbatim.
//
//goland:noinspection GoMixedReceiverTypes
func (e Syntax) WriteEntry(k, v string) (r string, err error) {
	switch e {
	case SyntaxDocker:
		// Docker does not accept multi-line environments
		if strings.IndexByte(v, '\n') >= 0 {
			return "", fmt.Errorf("docker run/build does not support multi-line environment values: key: %s, value %s", k, v)
		}
		r = fmt.Sprintf("%s=%s", k, v)
	case SyntaxCompose:
		r = fmt.Sprintf("%s=%s", k, quoteCompose(v))
	case SyntaxSh:
		r = fmt.Sprintf("%s=%s", k, shellquote.Unix(v))
	case SyntaxShExport:
		r = fmt.Sprintf("export %s=%s", k, shellquote.Unix(v))
	case SyntaxCsh:
		r = fmt.Sprintf("set %s=%s", k, quoteCsh(v))
	case SyntaxCshExport:
		r = fmt.Sprintf("setenv %s %s", k, quoteCsh(v))
	case SyntaxPS:
		r = fmt.Sprintf("$Env:%s=%s", k, quotePS(v))
	case SyntaxPSExport:
		r = fmt.Sprintf("[Environment]::SetEnvironmentVariable(%s, %s, 'User')", quotePS(k), quotePS(v))
	case SyntaxCmd:
		if strings.IndexByte(v, '\n') >= 0 {
			return "", fmt.Errorf("cmd does not support multi-line environment values: key: %s, value %s", k, v)
		}
		r = fmt.Sprintf("set %s=%s", k, v)
	case SyntaxJSON:
		return "", errors.New("WriteEntry isn't supported for json")
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

var cshEscaper = strings.NewReplacer("!", `\!`, "\n", "\\\n") //nolint:gochecknoglobals // constant

// quoteCsh quotes s like a POSIX shell would and then escapes what csh treats as special even
// within single quotes: '!' triggers history substitution and a newline ends the command.
func quoteCsh(s string) string {
	return cshEscaper.Replace(shellquote.Unix(s))
}

// quoteCompose returns s in a form that docker compose reads verbatim from an env_file. Double quotes
// are used when s contains a newline, carriage return, or tab, which are then written as escapes, or a
// backslash that precedes a single quote or ends s, which a single-quoted value can't represent.
func quoteCompose(s string) string {
	if s == "" {
		return ``
	}
	if strings.ContainsAny(s, "\n\t\r") || strings.HasSuffix(s, `\`) || strings.Contains(s, `\'`) {
		return quoteComposeDouble(s)
	}
	if !shellquote.UnixEscape.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `\'`) + "'"
}

// quoteComposeDouble escapes '$' to prevent the variable interpolation that docker compose applies
// to double-quoted values.
func quoteComposeDouble(s string) string {
	sb := strings.Builder{}
	sb.WriteByte('"')
	for _, c := range s {
		switch c {
		case '"', '\\', '$':
			sb.WriteByte('\\')
			sb.WriteRune(c)
		case '\n':
			sb.WriteString(`\n`)
		case '\t':
			sb.WriteString(`\t`)
		case '\r':
			sb.WriteString(`\r`)
		default:
			sb.WriteRune(c)
		}
	}
	sb.WriteByte('"')
	return sb.String()
}
