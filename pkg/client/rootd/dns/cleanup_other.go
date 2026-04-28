//go:build !linux

package dns

import "context"

// CleanupRouting removes DNS routing state that might have been left behind by
// a previous root daemon process.
func CleanupRouting(context.Context) {
}
