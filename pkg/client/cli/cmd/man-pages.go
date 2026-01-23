package cmd

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func manPages() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:  "man-pages",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return genMarkdown(cmd.Parent(), dir)
		},
		Hidden:        true,
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	flags := cmd.Flags()
	flags.StringVar(&dir, "dir", "/tmp", "Directory to write the manual page to")
	return cmd
}

func genMarkdown(cmd *cobra.Command, dir string) error {
	err := os.MkdirAll(dir, 0o755)
	if err != nil {
		return err
	}
	buf := &bytes.Buffer{}
	return genCommandMarkdown(cmd, dir, buf)
}

func entityEscape(s string, w *bytes.Buffer) {
	for _, c := range s {
		switch c {
		case '&':
			w.WriteString("&amp;")
		case '<':
			w.WriteString("&lt;")
		case '>':
			w.WriteString("&gt;")
		case '"':
			w.WriteString("&quot;")
		default:
			w.WriteRune(c)
		}
	}
}

// GenMarkdownCustom creates custom markdown output.
func genCommandMarkdown(cmd *cobra.Command, dir string, buf *bytes.Buffer) error {
	cmd.InitDefaultHelpFlag()

	// Create a font-matter header.
	buf.WriteString("---\ntitle: ")
	buf.WriteString(cmd.CommandPath())
	buf.WriteByte('\n')
	if cmd.Short != "" {
		buf.WriteString("description: ")
		entityEscape(cmd.Short, buf)
		buf.WriteByte('\n')
	}
	buf.WriteString("hide_table_of_contents: true\n---\n\n")
	if cmd.Short != "" {
		entityEscape(cmd.Short, buf)
		buf.WriteString("\n\n")
	}
	if cmd.Long != "" {
		buf.WriteString("## Synopsis:\n\n")
		entityEscape(cmd.Long, buf)
		buf.WriteString("\n\n")
	}

	entityEscape(cmd.UsageString(), buf)
	err := os.WriteFile(fmt.Sprintf("%s/%s.md", dir, strings.ReplaceAll(cmd.CommandPath(), " ", "_")), buf.Bytes(), 0o644)
	if err != nil {
		return err
	}
	for _, c := range cmd.Commands() {
		if c.Hidden || c.Name() == "help" {
			continue
		}
		buf.Reset()
		err = genCommandMarkdown(c, dir, buf)
		if err != nil {
			return err
		}
	}
	return nil
}
