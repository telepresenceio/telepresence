package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/pkg/errors"
	"github.com/spf13/pflag"

	"github.com/datawire/build-aux/bin-go/go-mkopensource/internal/golist"
)

type CLIArgs struct {
	OutputName     string
	OutputFilename string
	GoTarFilename  string
	Package        string
}

func parseArgs() (*CLIArgs, error) {
	args := &CLIArgs{}
	argparser := pflag.NewFlagSet(os.Args[0], pflag.ContinueOnError)
	help := false
	argparser.BoolVarP(&help, "help", "h", false, "Show this message")
	argparser.StringVar(&args.OutputFilename, "output", "", "")
	argparser.StringVar(&args.OutputName, "output-name", "", "")
	argparser.StringVar(&args.GoTarFilename, "gotar", "", "")
	argparser.StringVar(&args.Package, "package", "", "")
	if err := argparser.Parse(os.Args[1:]); err != nil {
		return nil, err
	}
	if help {
		fmt.Printf("Usage: %v OPTIONS\n", os.Args[0])
		fmt.Println("Build a .opensource.tar.gz tarball for open source license compliance")
		fmt.Println()
		fmt.Println("OPTIONS:")
		argparser.PrintDefaults()
		return nil, pflag.ErrHelp
	}
	if argparser.NArg() != 0 {
		return nil, errors.Errorf("expected 0 arguments, got %d: %q", argparser.NArg(), argparser.Args())
	}
	if args.OutputName == "" && args.OutputFilename == "" {
		return nil, errors.Errorf("at least one of --output= or --output-name= must be specified")
	}
	if args.OutputFilename != "" && !strings.HasSuffix(args.OutputFilename, ".tar.gz") {
		return nil, errors.Errorf("--output (%q) must have .tar.gz suffix", args.OutputFilename)
	}
	if args.OutputName == "" {
		args.OutputName = strings.TrimSuffix(filepath.Base(args.OutputFilename), ".tar.gz")
	}
	if !strings.HasPrefix(filepath.Base(args.GoTarFilename), "go1.") || !strings.HasSuffix(args.GoTarFilename, ".tar.gz") {
		return nil, errors.Errorf("--gotar (%q) doesn't look like a go1.*.tar.gz file", args.GoTarFilename)
	}
	if args.Package == "" {
		return nil, errors.Errorf("--package (%q) must be non-empty", args.Package)
	}
	return args, nil
}

func main() {
	args, err := parseArgs()
	if err != nil {
		if err == pflag.ErrHelp {
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "%s: %v\nTry '%s --help' for more information.\n", os.Args[0], err, os.Args[0])
		os.Exit(2)
	}
	if err := Main(args); err != nil {
		fmt.Fprintf(os.Stderr, "%s: fatal: %v\n", os.Args[0], err)
		os.Exit(1)
	}
}

func loadGoTar(goTarFilename string) (version string, license []byte, err error) {
	goTarFile, err := os.Open(goTarFilename)
	if err != nil {
		return "", nil, err
	}
	defer goTarFile.Close()
	goTarUncompressed, err := gzip.NewReader(goTarFile)
	if err != nil {
		return "", nil, err
	}
	defer goTarUncompressed.Close()
	goTar := tar.NewReader(goTarUncompressed)
	for {
		header, err := goTar.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			return "", nil, err
		}
		switch header.Name {
		case "go/VERSION":
			fc, err := ioutil.ReadAll(goTar)
			if err != nil {
				return "", nil, err
			}
			version = "v" + strings.TrimPrefix(strings.TrimSpace(string(fc)), "go")
		case "go/LICENSE":
			fc, err := ioutil.ReadAll(goTar)
			if err != nil {
				return "", nil, err
			}
			license = fc
		}
		if version != "" && license != nil {
			break
		}
	}
	if version == "" || license == nil {
		return "", nil, errors.Errorf("file %q did not contain %q or %q", goTarFilename, "go/VERSION", "go/LICENSE")
	}
	return version, license, nil
}

func dirForModule(tarfiles map[string][]byte, modname string) error {
	fileinfos, err := ioutil.ReadDir("vendor/" + modname)
	if err != nil {
		return err
	}
	licensePrefixes := []string{
		"LICENSE",
		"license",
		"COPYING",
		"copying",
	}
	var license []string
	for _, fileinfo := range fileinfos {
		for _, prefix := range licensePrefixes {
			if strings.HasPrefix(fileinfo.Name(), prefix) {
				license = append(license, fileinfo.Name())
			}
		}
	}
	switch len(license) {
	case 0:
		return errors.Errorf("%q has no LICENSE file", modname)
	case 1:
		// do nothing
	default:
		fmt.Fprintf(os.Stderr, "WARNING: %q: found %d LICENSE files, heuristics more likely to be wrong\n", modname, len(license))
	}
	licenseBody, err := ioutil.ReadFile("vendor/" + modname + "/" + license[0])
	if err != nil {
		return err
	}
	if strings.HasPrefix(string(licenseBody), "Mozilla Public License, version 2.0") {
		// Copyleft
		err := filepath.Walk("vendor/"+modname, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			body, err := ioutil.ReadFile(path)
			if err != nil {
				return err
			}
			tarfiles[strings.TrimPrefix(path, "vendor/")] = body
			return nil
		})
		if err != nil {
			return err
		}
	} else {
		// Permissive
		extraPrefixes := []string{
			"NOTICE", // required for Apache 2.0 compliance
			"PATENT", // for BSD and MIT, which do not include a patent grant
		}
		var extrafiles []string
		for _, fileinfo := range fileinfos {
			for _, prefix := range extraPrefixes {
				if strings.HasPrefix(fileinfo.Name(), prefix) {
					extrafiles = append(extrafiles, fileinfo.Name())
				}
			}
		}
		for _, filename := range append(license, extrafiles...) {
			body, err := ioutil.ReadFile("vendor/" + modname + "/" + filename)
			if err != nil {
				return err
			}
			tarfiles[modname+"/"+filename] = body
		}
	}
	return nil
}

func Main(args *CLIArgs) error {
	// go list (& post-processing)
	listPkgs, err := golist.GoList(args.Package, "-deps")
	if err != nil {
		return err
	}
	var gopkg, gomod string
	for _, pkg := range listPkgs {
		if !pkg.DepOnly {
			gopkg = pkg.ImportPath
			gomod = pkg.Module.Path
		}
	}
	if gopkg == "" || gomod == "" {
		return errors.New("go list didn't give us the requested package")
	}
	listMods := make(map[string]*golist.Module)
	for _, pkg := range listPkgs {
		key := "<nil>"
		if pkg.Module != nil {
			key = pkg.Module.Path
			if pkg.Module.Path == gomod || pkg.Module.Path == "github.com/datawire/liboauth2" || pkg.Module.Path == "github.com/datawire/teleproxy" {
				continue
			}
		}
		if _, done := listMods[key]; done {
			continue
		}
		listMods[key] = pkg.Module
	}

	// tar xf go{version}.src.tar.gz
	goVersion, goLicense, err := loadGoTar(args.GoTarFilename)
	if err != nil {
		return err
	}

	// gather files...
	files := make(map[string][]byte)
	readme := new(bytes.Buffer)
	readme.WriteString(wordwrap(75, fmt.Sprintf("The program %q incorporates the following Free and Open Source software:", path.Base(gopkg))))
	readme.WriteString("\n")
	table := tabwriter.NewWriter(readme, 0, 8, 2, ' ', 0)
	io.WriteString(table, "  \tName\tVersion\n")
	io.WriteString(table, "\t----\t-------\n")
	modNames := make([]string, 0, len(listMods))
	for k := range listMods {
		modNames = append(modNames, k)
	}
	sort.Strings(modNames)
	for _, modKey := range modNames {
		modVal := listMods[modKey]
		var depName, depVersion string
		if modVal == nil {
			depName = "the Go language standard library (\"std\")"
			depVersion = goVersion
			files["std/LICENSE"] = goLicense
		} else {
			depName = modVal.Path
			depVersion = modVal.Version
			if modVal.Replace != nil {
				if modVal.Replace.Version == "" {
					depVersion = "(modified)"
				} else {
					if modVal.Replace.Path != modVal.Path {
						depName = fmt.Sprintf("%s (modified from %s)", modVal.Replace.Path, modVal.Path)
					}
					depVersion = modVal.Replace.Version
				}
			}
			if err := dirForModule(files, modVal.Path); err != nil {
				return err
			}
		}
		// TODO: license+files
		fmt.Fprintf(table, "\t%s\t%s\n", depName, depVersion)
	}
	table.Flush()
	readme.WriteString("\n")
	readme.WriteString(wordwrap(75, "The appropriate license notices and source code are in correspondingly named directories."))
	files["OPENSOURCE.md"] = readme.Bytes()

	// write output
	var outputFile *os.File
	if args.OutputFilename == "" {
		outputFile = os.Stdout
	} else {
		outputFile, err = os.OpenFile(args.OutputFilename, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0666)
		if err != nil {
			return err
		}
	}
	defer outputFile.Close()
	outputCompressed := gzip.NewWriter(outputFile)
	defer outputCompressed.Close()
	outputTar := tar.NewWriter(outputCompressed)
	defer outputTar.Close()

	filenames := make([]string, 0, len(files))
	for filename := range files {
		filenames = append(filenames, filename)
	}
	sort.Strings(filenames)
	for _, filename := range filenames {
		body := files[filename]
		err := outputTar.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     args.OutputName + "/" + filename,
			Size:     int64(len(body)),
			Mode:     0644,
		})
		if err != nil {
			return err
		}
		if _, err := outputTar.Write(body); err != nil {
			return err
		}
	}
	return nil
}
