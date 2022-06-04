package watchable_test

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"
)

func assertDeepCopies(t *testing.T, a, b proto.Message, msgAndArgs ...any) bool {
	t.Helper()
	if a == b {
		return assert.Fail(t,
			fmt.Sprintf("Objects are pointer equal (are not copies):\n"+
				"a: %p\n"+
				"b: %p\n",
				a, b),
			msgAndArgs...)
	}
	if diff := cmp.Diff(a, b, cmp.Comparer(proto.Equal)); diff != "" {
		return assert.Fail(t, "Not equal (-expected +actual):\n"+diff, msgAndArgs...)
	}
	return true
}
