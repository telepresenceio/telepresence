package types

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// CommandInfo describes a command and its hierarchy.
type CommandInfo struct {
	Name        string        `json:"name"`
	Usage       string        `json:"usage,omitempty"`
	Description string        `json:"description,omitempty"`
	Flags       []FlagInfo    `json:"flags,omitempty"`
	Subcommands []CommandInfo `json:"subcommands,omitempty"`
}

// FlagInfo describes a flag parsed from help text.
type FlagInfo struct {
	Name        string `json:"name,omitempty"`
	Shorthand   string `json:"shorthand,omitempty"`
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
	Default     string `json:"default,omitempty"` // parsed from "(default ...)"
	Global      bool   `json:"global,omitempty"`
	Inherited   bool   `json:"inherited,omitempty"`
}

// HelpFetcher fetches help text for a command path (root and subcommands).
type HelpFetcher struct {
	Executable string
	HelpArg    string
}

func (f *HelpFetcher) fetch(path []string) (string, error) {
	args := make([]string, 0, len(path)+1)
	args = append(args, path...)
	args = append(args, f.HelpArg)
	cmd := exec.Command(f.Executable, args...)
	cmd.Env = os.Environ()

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	out := buf.String()
	// Some binaries return non-zero for --help; accept if output looks like help.
	if err != nil {
		if looksLikeHelp(out) {
			return out, nil
		}
		return "", fmt.Errorf("error running %q %v: %w\nOutput:\n%s", f, args, err, out)
	}
	return out, nil
}

func looksLikeHelp(s string) bool {
	return strings.Contains(s, "Usage:") || strings.Contains(s, "Commands:")
}

// Parse and traverse

// BuildCommandTree recursively builds the command tree starting at path.
func (f *HelpFetcher) BuildCommandTree(path []string, maxDepth int) (*CommandInfo, error) {
	text, err := f.fetch(path)
	if err != nil {
		return nil, err
	}

	info := &CommandInfo{}
	if len(path) > 0 {
		info.Name = path[len(path)-1]
	} else {
		info.Name = f.Executable
	}
	info.Usage = parseUsage(text)
	info.Description = parseShortDescription(text)

	flags := make([]FlagInfo, 0, 16)
	flags = append(flags, parseFlagsFromSection(text, false, false, `(?m:^(?:Flags|Options):)`)...)
	flags = append(flags, parseFlagsFromSection(text, true, false, `(?m:^Global (?:Flags|Options):)`)...)
	// Cobra shows inherited flags on subcommands using this heading:
	flags = append(flags, parseFlagsFromSection(text, false, true, `(?m:^(?:Flags|Options) inherited from parent commands:)`)...)
	// Some Cobra setups may use alternate headings; add heuristics if needed.
	info.Flags = flags

	// Discover subcommands
	subCommands := parseAvailableCommands(text)
	if len(subCommands) == 0 || maxDepth < 1 {
		return info, nil
	}

	children := make([]CommandInfo, 0, len(subCommands))
	for _, name := range subCommands {
		if name == info.Name {
			// We consider it highly unlikely that a command will be named the same as its parent. This assumption
			// prevents us from recursing down in docker swarm init [init...]
			continue
		}
		subPath := append(path, name)
		child, err := f.BuildCommandTree(subPath, maxDepth-1)
		if err != nil {
			return nil, err
		}
		children = append(children, *child)
	}
	info.Subcommands = children
	return info, nil
}

// Helpers to parse sections

func parseUsage(text string) string {
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "Usage:") || strings.HasPrefix(t, "usage:") {
			return strings.TrimSpace(t[len("Usage:"):])
		}
	}
	return ""
}

var afterUsagePrefixes = []string{
	"Available Commands:",
	"Flags:",
	"Global Flags:",
	"Flags inherited from parent commands:",
	"Commands:",
	"Options:",
	"Global Options:",
	"Options inherited from parent commands:",
}

func parseShortDescription(text string) string {
	for _, t := range strings.Split(text, "\n") {
		t = strings.TrimSpace(t)
		if t == "" || strings.HasPrefix(t, "Usage:") {
			continue
		}
		for _, prefix := range afterUsagePrefixes {
			if strings.HasPrefix(t, prefix) {
				// We've gone too far.
				return ""
			}
		}
		// Take this as a description and stop.
		return t
	}
	return ""
}

func parseAvailableCommands(text string) []string {
	var res []string
	for {
		text = sliceAfter(text, `(?m:^(?:[A-Z][0-9A-Za-z-]*\s+)?Commands:)`)
		if text == "" {
			break
		}
		for _, line := range strings.Split(text, "\n") {
			trim := strings.TrimSpace(line)
			if trim == "" {
				continue
			}
			if !startsWithIndent(line) {
				break
			}
			name := trim
			spaceIdx := strings.IndexAny(trim, " \t\n")
			if spaceIdx > 0 {
				name = trim[:spaceIdx]
			}
			name = strings.TrimSuffix(name, "*")
			res = append(res, name)
		}
	}
	return res
}

func startsWithIndent(s string) bool {
	return strings.HasPrefix(s, "  ") || strings.HasPrefix(s, "\t")
}

var (
	// Matches lines like:
	//   -n, --name string   description (default "foo")
	//       --verbose       description
	//       --count int     description (default 3)
	//   -q                   description
	flagLineRe = regexp.MustCompile(`^(?:-([A-Za-z0-9]),\s*)?(?:--([A-Za-z0-9][A-Za-z0-9-]*))?(?:\s+(\S+))?\s{2,}(.+)$`)
	defaultRe  = regexp.MustCompile(`^(.*)\s*\((?i:default)[:\s]*([^)]+)\)`)
)

func parseFlagsFromSection(text string, global, inherited bool, headerPattern string) []FlagInfo {
	section := sliceAfter(text, headerPattern)
	if section == "" {
		return nil
	}
	lines := strings.Split(section, "\n")
	var flags []FlagInfo
	for _, line := range lines {
		// Expect flag lines to be indented.
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		if !startsWithIndent(line) {
			break
		}
		line = trim
		m := flagLineRe.FindStringSubmatch(line)
		if len(m) == 0 {
			continue
		}
		short := m[1]
		long := m[2]
		typ := m[3]
		if typ == "" {
			typ = "bool"
		}
		desc := strings.TrimSpace(m[4])

		if long == "" && short == "" {
			continue
		}

		dflt := ""
		if mm := defaultRe.FindStringSubmatch(desc); len(mm) == 3 {
			// This might be a "fake" default, i.e. a default that is embedded in the description rather
			// than a true default for the flag. We don't want defaults that are unparseable.
			dflt = strings.TrimSpace(mm[2])
			switch typ {
			case "int", "int16", "int32", "int64", "uint16", "uint32", "uint64":
				if _, err := strconv.ParseInt(dflt, 10, 64); err != nil {
					dflt = ""
				}
			case "bool":
				if _, err := strconv.ParseBool(dflt); err != nil {
					dflt = ""
				}
			case "duration":
				if _, err := time.ParseDuration(dflt); err != nil {
					dflt = ""
				}
			case "float32", "float64":
				if _, err := strconv.ParseFloat(dflt, 64); err != nil {
					dflt = ""
				}
			case "string":
				if len(dflt) < 3 || dflt[0] != '"' || dflt[len(dflt)-1] != '"' {
					dflt = ""
				}
			}
			if dflt != "" {
				desc = strings.TrimSpace(mm[1])
			}
		}

		flags = append(flags, FlagInfo{
			Name:        long,
			Shorthand:   short,
			Type:        typ,
			Description: desc,
			Default:     dflt,
			Global:      global,
			Inherited:   inherited,
		})
	}
	return flags
}

func sliceAfter(text, headerPattern string) string {
	if ixs := regexp.MustCompile(headerPattern).FindStringIndex(text); ixs != nil {
		return text[ixs[1]:]
	}
	return ""
}
