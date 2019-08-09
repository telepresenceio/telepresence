package main

import (
	"strings"
)

func wordwrap(width int, str string) string {
	var words []string
	for len(str) > 0 {
		sep := strings.IndexAny(str, " \n")
		if sep < 0 {
			words = append(words, str)
			break
		}
		word := str[:sep]
		rest := str[sep+1:]
		words = append(words, word)
		// First space after a period is a non-breaking space;
		// encode that with an empty word.
		if strings.HasSuffix(word, ".") && strings.HasPrefix(rest, " ") {
			words = append(words, "")
		}
		str = strings.TrimLeft(rest, " \n")
	}
	linewidth := 0
	ret := new(strings.Builder)
	sep := ""
	for _, word := range words {
		if word == "" {
			sep = "  "
		} else if linewidth+len(sep)+len(word) > width {
			ret.WriteString("\n")
			ret.WriteString(word)
			linewidth = len(word)
			sep = " "
		} else {
			ret.WriteString(sep)
			ret.WriteString(word)
			linewidth += len(sep) + len(word)
			sep = " "
		}
	}
	ret.WriteString("\n")
	return ret.String()
}
