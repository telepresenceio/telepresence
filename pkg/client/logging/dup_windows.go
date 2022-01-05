package logging

import (
	"os"

	"golang.org/x/sys/windows"
)

func dupToStd(file *os.File) error {
	// https://stackoverflow.com/questions/34772012/capturing-panic-in-golang/34772516

	fd := file.Fd()

	if err := windows.SetStdHandle(windows.STD_OUTPUT_HANDLE, windows.Handle(fd)); err != nil {
		return err
	}
	if err := windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(fd)); err != nil {
		return err
	}

	os.Stdout = file
	os.Stderr = file

	return nil
}
