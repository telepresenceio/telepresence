package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"sigs.k8s.io/yaml"
)

func main() {
	var input string
	flag.StringVar(&input, "input", "", "input file")
	flag.Parse()
	err := makeTOC(input)
	if err != nil {
		log.Fatal(err)
	}
}

type TOC struct {
	Title string `json:"title"`
	Link  string `json:"link,omitempty"`
	Items []TOC  `json:"items,omitempty"`
}

func readTOC(input string) ([]*TOC, error) {
	data, err := os.ReadFile(input)
	if err != nil {
		return nil, err
	}
	var toc []*TOC
	err = yaml.Unmarshal(data, &toc)
	if err != nil {
		return nil, err
	}
	return toc, nil
}

func (t *TOC) writeMarkdown(indent string, w io.Writer) (err error) {
	if t.Link == "" {
		_, err = fmt.Fprintf(w, "%s- %s\n", indent, t.Title)
	} else {
		_, err = fmt.Fprintf(w, "%s- [%s](%s.md)\n", indent, t.Title, strings.TrimSuffix(t.Link, "/"))
	}
	if err == nil && len(t.Items) > 0 {
		indent = "  " + indent
		for _, c := range t.Items {
			if err = c.writeMarkdown(indent, w); err != nil {
				break
			}
		}
	}
	return err
}

const frontMatter = `---
description: Main menu when using plain markdown. Excluded when generating the website
---
# <img src="images/logo.png" height="64px"/> Telepresence Documentation
raw markdown version, more bells and whistles at [telepresence.io](https://telepresence.io)

`

func makeTOC(input string) error {
	ts, err := readTOC(input)
	if err != nil {
		return err
	}
	wr := bufio.NewWriter(os.Stdout)
	_, err = wr.WriteString(frontMatter)
	if err != nil {
		return err
	}
	for _, c := range ts {
		if err = c.writeMarkdown("", wr); err != nil {
			return err
		}
	}
	return wr.Flush()
}
