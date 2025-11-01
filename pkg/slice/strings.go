package slice

import "fmt"

func AsStrings[T fmt.Stringer](vs []T) []string {
	ss := make([]string, len(vs))
	for i, v := range vs {
		ss[i] = v.String()
	}
	return ss
}
