//go:build embed_fuseftp && !docker

package remotefs

import (
	"context"
	_ "embed"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

//go:embed fuseftp.bits
var fuseftpBits []byte

func getFuseFTPServer(ctx context.Context, exe string) (string, error) {
	qn := filepath.Join(filelocation.AppUserCacheDir(ctx), exe)
	var sz int
	st, err := os.Stat(qn)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		sz = 0
	} else {
		sz = int(st.Size())
	}
	if len(fuseftpBits) != sz {
		err = os.WriteFile(qn, fuseftpBits, 0o700)
	}
	return qn, err
}
