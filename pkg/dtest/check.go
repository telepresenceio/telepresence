package dtest

import (
	"log"
	"os"
)

// The check function logs any non-nil error arguments and exits with
// error code 1 unless all arguments are nil. If all arguments are
// nil, then it is a noop.
func Check(errors ...error) {
	exit := false
	for _, err := range errors {
		if err != nil {
			log.Println(err)
			exit = true
		}
	}

	if exit {
		os.Exit(1)
	}
}
