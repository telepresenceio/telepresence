package integration_test

import (
	"os"
)

// Test_IntentionalCIRetryFlake exists only to validate the
// check-integration-ci retry orchestration on a draft PR and is removed
// before merge. It fails on the first attempt (no attempt-1 failures file
// exists yet in the workspace root) and passes on a retry, so a green run
// proves that attempt 1 ran to completion, the retry was scoped to this
// test, and the summary labels it flaky.
func (s *httpInterceptsSuite) Test_IntentionalCIRetryFlake() {
	if _, err := os.Stat("../check-integration-attempt-1-failures.log"); err != nil {
		s.Fail("intentional first-attempt failure for retry validation")
	}
}
