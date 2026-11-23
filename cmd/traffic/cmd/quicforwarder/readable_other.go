//go:build !linux

package quicforwarder

// readable reports true, so that the batch read itself waits for data.
func readable(uintptr) bool { return true }
