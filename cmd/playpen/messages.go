package main

// EmptyArgs supports RPCs that take no arguments
type EmptyArgs struct{}

// StringReply supports RPCs that return a single string
type StringReply struct {
	Message string
}

// ConnectArgs are used by the connect command
type ConnectArgs struct {
	RAI   *RunAsInfo // How to run commands as the user
	KArgs []string   // Additional arguments for kubectl
}
