// The docslint command checks the documentation tree for consistency:
//
//  1. Every link in doc-links.yml points to an existing page.
//  2. Every page is reachable from doc-links.yml (no orphans).
//  3. Every redirect target in redirects.yml points to an existing page.
//  4. Every relative link in the markdown files resolves to an existing file.
//
// Usage: docslint [docs-directory]
package main

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"
)

type entry struct {
	Title string  `json:"title"`
	Link  string  `json:"link,omitempty"`
	Items []entry `json:"items,omitempty"`
}

type redirect struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// pages that are intentionally not part of the doc-links.yml navigation.
var navExempt = map[string]bool{ //nolint:gochecknoglobals // constant
	"README.md":         true, // generated from doc-links.yml
	"CONTRIBUTING.md":   true, // instructions for the telepresence.io repository
	"release-notes.mdx": true, // website variant of release-notes.md
}

// pages whose links are not checked: the release notes are generated from
// immutable history, and their older entries link to pages that no longer
// exist.
var linkExempt = map[string]bool{ //nolint:gochecknoglobals // constant
	"release-notes.md":  true,
	"release-notes.mdx": true,
}

// directories whose pages are not part of the navigation.
var navExemptDirs = []string{"plans", "common"} //nolint:gochecknoglobals // constant

func main() {
	root := "docs"
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	var problems []string
	report := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	pageExists := func(link string) bool {
		_, err := os.Stat(filepath.Join(root, link+".md"))
		return err == nil
	}

	// 1. doc-links.yml links resolve, and collect them for the orphan check.
	linked := make(map[string]bool)
	var walkEntries func([]entry)
	walkEntries = func(es []entry) {
		for _, e := range es {
			if e.Link != "" && !strings.Contains(e.Link, "://") {
				linked[e.Link] = true
				if !pageExists(e.Link) {
					report("doc-links.yml: link %q has no page %s.md", e.Link, e.Link)
				}
			}
			walkEntries(e.Items)
		}
	}
	var entries []entry
	readYAML(filepath.Join(root, "doc-links.yml"), &entries, report)
	walkEntries(entries)

	// 3. redirects.yml targets resolve.
	var redirects []redirect
	readYAML(filepath.Join(root, "redirects.yml"), &redirects, report)
	for _, r := range redirects {
		if r.To != "" && !strings.Contains(r.To, "://") && !pageExists(r.To) {
			report("redirects.yml: redirect from %q targets nonexistent page %s.md", r.From, r.To)
		}
	}

	// 2 and 4. Walk the pages.
	linkRx := regexp.MustCompile(`]\(([^()\s]+)\)`)
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if slices.Contains(navExemptDirs, rel) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".md") {
			return nil
		}

		// 2. Orphan check.
		if !navExempt[rel] && !linked[strings.TrimSuffix(rel, ".md")] {
			report("%s: page is not reachable from doc-links.yml", rel)
		}

		// 4. Relative links resolve.
		if linkExempt[rel] {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range linkRx.FindAllStringSubmatch(stripCodeFences(string(raw)), -1) {
			target := m[1]
			if i := strings.IndexByte(target, '#'); i >= 0 {
				target = target[:i]
			}
			switch {
			case target == "",
				strings.Contains(target, "://"),
				strings.HasPrefix(target, "mailto:"),
				strings.HasPrefix(target, "/"): // site-absolute; owned by the website
				continue
			}
			resolved := path.Join(path.Dir(rel), target)
			if path.Ext(resolved) == "" {
				resolved += ".md"
			}
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(resolved))); err != nil {
				report("%s: broken link %q", rel, m[1])
			}
		}
		return nil
	})

	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, p)
		}
		fmt.Fprintf(os.Stderr, "docslint: %d problem(s) found\n", len(problems))
		os.Exit(1)
	}
}

func readYAML(file string, into any, report func(string, ...any)) {
	raw, err := os.ReadFile(file)
	if err != nil {
		report("%s: %v", file, err)
		return
	}
	if err := yaml.Unmarshal(raw, into); err != nil {
		report("%s: %v", file, err)
	}
}

// stripCodeFences blanks out fenced code blocks so that link-like text in
// examples is not validated.
func stripCodeFences(s string) string {
	var b strings.Builder
	fence := false
	for line := range strings.SplitSeq(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fence = !fence
			b.WriteString("\n")
			continue
		}
		if fence {
			b.WriteString("\n")
		} else {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	return b.String()
}
