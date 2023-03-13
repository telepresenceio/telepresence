package intercept

import (
	"fmt"
	"os"

	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

func PrepareMount(_ string, mountPoint string) (string, error) {
	var err error
	if mountPoint == "" {
		// Find a free drive letter. Background at T, loop around and skip C and D,
		// A and B aren't often used nowadays. No floppy-disks.
		for _, c := range "TUVXYZABEFGHIJKLMNOPQR" {
			_, err = os.Stat(fmt.Sprintf(`%c:\`, c))
			if os.IsNotExist(err) {
				return fmt.Sprintf(`%c:`, c), nil
			}
		}
		return "", errcat.User.New("found no available drive to use as mount point")
	}

	// Mount point must be a drive letter
	ok := len(mountPoint) == 2 && mountPoint[1] == ':'
	if ok {
		dl := mountPoint[0]
		ok = dl >= 'A' && dl <= 'Z' || dl >= 'a' && dl <= 'z'
	}
	if !ok {
		err = errcat.User.New("mount point must be a drive letter followed by a colon")
	}
	return mountPoint, err
}
