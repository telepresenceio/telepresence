package main

import "fmt"

type Result struct {
	Report   string
	Message  string
	TryAgain bool
	URL      string
	Err      error
}

func UnhandledErrResult(err error) Result {
	return Result{
		Report:  "",
		Message: "",
		Err:     err,
	}
}
