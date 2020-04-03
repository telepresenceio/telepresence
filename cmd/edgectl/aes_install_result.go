package main

import (
	"bytes"
	"fmt"
	"text/template"

	"github.com/gookit/color"
	"github.com/pkg/browser"
)

// Result represents the result of an installation attempt
type Result struct {
	Report   string // Action to report to Metriton
	Message  string // Message to show to the user
	TryAgain bool   // Whether to show the "try again" message (TODO: Is this necessary?)
	URL      string // Docs URL to show and open
	Err      error  // Error condition (nil -> successful installation)
}

// UnhandledErrResult returns a minimal Result that passes along an error but
// otherwise does not do anything. It is intended for transitional use, until
// *every* error is handled explicitly to yield a meaningful Result.
func UnhandledErrResult(err error) Result {
	return Result{
		Err: err,
	}
}

func (i *Installer) ShowResult(r Result) {
	if r.Err != nil {
		// Failure

		i.log.Printf("Result: Installation failed")
		i.log.Printf(" Error: %+v", r.Err)

		if r.Report != "" {
			i.Report(r.Report, ScoutMeta{"err", r.Err.Error()})
		}

		if r.Message != "" {
			i.show.Println()
			i.show.Println("AES Installation Unsuccessful")
			i.show.Println("========================================================================")
			i.show.Println()
			i.ShowWrapped(r.Message)

			if r.URL != "" {
				i.show.Println()
				i.ShowWrapped(fmt.Sprintf("Visit %s for more information and instructions.", r.URL))

				if err := browser.OpenURL(r.URL); err != nil {
					i.log.Printf("Failed to open browser: %+v", err)
				}
			}

			if r.TryAgain {
				i.show.Println()
				i.ShowWrapped(tryAgain)
			}
		}

	} else {
		// Success

		if r.Report != "" {
			i.Report(r.Report)
		}

		if r.Message != "" {
			i.show.Println()
			i.show.Println("AES Installation Complete!")
			i.show.Println("========================================================================")
			i.show.Println()
			i.ShowTemplated(r.Message)

			// Assume there is no URL to open automatically. The login code may
			// be invoked; it opens the browser itself.

			// Assume there's no need to show the "try again" message.
		}
	}
}

// AdditionalDatum represents a key-value pair that may be used in template
// expansion
type AdditionalDatum struct {
	key   string
	value interface{}
}

// ShowTemplated displays a string to the user (using ShowWrapped) after
// rendering the supplied text as a template using values from the installer
// object and the additional parameters. It also processes color tags.
// TODO: Fix color tag processing so it is effective on Windows.
func (i *Installer) ShowTemplated(text string, more ...AdditionalDatum) {
	t := template.New("installer")
	template.Must(t.Parse(text))

	data := map[string]interface{}{
		"version":   i.version,
		"address":   i.address,
		"hostname":  i.hostname,
		"clusterID": i.clusterID,
		"kubectl":   i.k8sVersion.Client.GitVersion,
		"k8s":       i.k8sVersion.Server.GitVersion,
	}
	for _, ad := range more {
		data[ad.key] = ad.value
	}

	templateBuffer := &bytes.Buffer{}
	if err := t.Execute(templateBuffer, data); err != nil {
		//i.log.Printf("WARNING: failed to render template: %+v", err)
		//return text
		panic(err) // An error here indicates a bug in our code
	}

	i.ShowWrapped(color.Render(templateBuffer.String()))
}
