package golist

import (
	"bytes"
	"encoding/json"
	"io"
	"os/exec"
)

func GoList(pkg string, flags ...string) ([]Package, error) {
	stdoutBytes, err := exec.Command("go", append([]string{"list"}, append(flags, "-json", "--", pkg)...)...).Output()
	if err != nil {
		return nil, err
	}
	stdoutDecoder := json.NewDecoder(bytes.NewReader(stdoutBytes))
	var ret []Package
	for {
		var pkg Package
		if err := stdoutDecoder.Decode(&pkg); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		ret = append(ret, pkg)
	}
	return ret, nil
}
