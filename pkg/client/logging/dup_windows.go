package logging

import (
	"os"

	"golang.org/x/sys/windows"
)

func dupToStdOut(file *os.File) error {
	if err := windows.SetStdHandle(windows.STD_OUTPUT_HANDLE, windows.Handle(file.Fd())); err != nil {
		return err
	}
	os.Stdout = file
	return nil
}

func dupToStdErr(file *os.File) error {
	// https://stackoverflow.com/questions/34772012/capturing-panic-in-golang/34772516
	if err := windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(file.Fd())); err != nil {
		return err
	}
	os.Stderr = file
	return nil
}
