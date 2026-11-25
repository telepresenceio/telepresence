package filelocation

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sync"

	"github.com/telepresenceio/clog"
)

// UntrustedError is returned by CheckManaged when a managed file, or one of the directories above it, can be
// changed by someone other than the administrator.
type UntrustedError struct {
	Path   string
	Reason string
}

func (e *UntrustedError) Error() string {
	return fmt.Sprintf("%s is not trusted: %s", e.Path, e.Reason)
}

// CheckManaged verifies that the file at path, and every directory from it up to and including root, can be
// trusted as installed by the administrator. A missing file is reported as an error that wraps fs.ErrNotExist.
// A file that cannot be trusted is reported as an *UntrustedError.
func CheckManaged(path, root string) error {
	return checkManaged(path, root)
}

var warned sync.Map //nolint:gochecknoglobals // one warning per path and process

// TrustedManagedFile returns nil when the managed file at path can be used. When it can't be trusted, one warning
// per process is logged and the returned error wraps fs.ErrNotExist, so that callers treat the file as absent.
func TrustedManagedFile(ctx context.Context, path, root string) error {
	err := CheckManaged(path, root)
	if ue := (*UntrustedError)(nil); errors.As(err, &ue) {
		if _, seen := warned.LoadOrStore(path, struct{}{}); !seen {
			clog.Warnf(ctx, "ignoring %s: %s", ue.Path, ue.Reason)
		}
		return fmt.Errorf("%w: %w", fs.ErrNotExist, err)
	}
	return err
}
