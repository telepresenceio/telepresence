package itest

import (
	"context"
	"io"
	"path/filepath"
	"text/template"

	"github.com/datawire/dlib/dlog"
)

func OpenTemplate(ctx context.Context, name string, data any) (io.ReadCloser, error) {
	tpl, err := template.New("").ParseFiles(filepath.Join(GetWorkingDir(ctx), name))
	if err != nil {
		return nil, err
	}
	rd, wr := io.Pipe()
	go func() {
		defer wr.Close()
		if err := tpl.ExecuteTemplate(wr, filepath.Base(name), data); err != nil {
			dlog.Errorf(ctx, "failed to read template %s: %v", name, err)
		}
	}()
	return rd, nil
}
