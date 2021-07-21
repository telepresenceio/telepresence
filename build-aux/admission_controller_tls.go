//+build ignore

package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"

	"github.com/telepresenceio/telepresence/v2/pkg/install"
)

// The program creates the crt.pem, key.pem, and ca.pem needed when
// setting up the mutator webhook for agent auto injection
func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "usage: %s <manager-namespace> <directory>", os.Args[0])
		os.Exit(1)
	}
	if err := generateKeys(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func generateKeys(mgrNamespace, dir string) error {
	err := os.MkdirAll(dir, 0777)
	if err != nil {
		return fmt.Errorf("failed to create directory %q: %w", dir, err)
	}
	crtPem, keyPem, caPem, err := install.GenerateKeys(mgrNamespace)
	if err != nil {
		return err
	}

	if err = writeFile(dir, "ca.pem", caPem); err != nil {
		return err
	}

	if err = writeFile(dir, "crt.pem", crtPem); err != nil {
		return err
	}
	return writeFile(dir, "key.pem", keyPem)
}

// writeFile writes the file verbatim and as base64 encoded in the given directory
func writeFile(dir, file string, data []byte) error {
	filePath := filepath.Join(dir, file)
	f, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("failed to create file %q, %w", filePath, err)
	}
	defer f.Close()

	if _, err = f.Write(data); err != nil {
		return fmt.Errorf("failed to write to file %q, %w", filePath, err)
	}

	filePath64 := filePath + ".base64"
	f64, err := os.Create(filePath64)
	if err != nil {
		return fmt.Errorf("failed to create file %q, %w", filePath64, err)
	}
	defer f64.Close()

	enc := base64.NewEncoder(base64.StdEncoding, f64)
	defer enc.Close()
	if _, err = enc.Write(data); err != nil {
		return fmt.Errorf("failed to write to file %q, %w", filePath64, err)
	}
	return nil
}
