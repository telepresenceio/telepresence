package quicfwd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// readTestdataHex reads testdata/name and returns its contents as a single hex string
// with all whitespace removed, so vectors can be wrapped across lines for readability.
func readTestdataHex(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return strings.Join(strings.Fields(string(b)), "")
}
