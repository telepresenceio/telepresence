package main

// EmptyArgs supports RPCs that take no arguments
type EmptyArgs struct{}

// StringReply supports RPCs that return a single string
type StringReply struct {
	Message string
}
