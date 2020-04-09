package main

import (
	"bytes"
	"text/template"

	"github.com/gookit/color"
	"github.com/pkg/browser"
)

// Result represents the result of an installation attempt
type Result struct {
	Report       string // Action to report to Metriton
	ShortMessage string // Short human-readable error message
	Message      string // Message to show to the user
	TryAgain     bool   // Whether to show the "try again" message (TODO: Is this necessary?)
	URL          string // Docs URL to show and open
	Err          error  // Error condition (nil -> successful installation)
}

// UnhandledErrResult returns a minimal Result that passes along an error but
// otherwise does not do anything. It is intended for transitional use, until
// *every* error is handled explicitly to yield a meaningful Result.
func UnhandledErrResult(err error) Result {
	return Result{
		Err: err,
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

	// Note: May fail before we have k8sVersion, so handle this special case.
	var k8sClientVersion = "unknown"
	var k8sServerVersion = "unknown"

	// Assign if we have k8sVersion available.
	if i.k8sVersion.Server.GitVersion != "" {
		k8sClientVersion = i.k8sVersion.Client.GitVersion
		k8sServerVersion = i.k8sVersion.Server.GitVersion
	}

	data := map[string]interface{}{
		"version":   i.version,
		"address":   i.address,
		"hostname":  i.hostname,
		"clusterID": i.clusterID,
		"kubectl":   k8sClientVersion,
		"k8s":       k8sServerVersion,
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
func (i *Installer) ShowResult(r Result) {
	templateData := []AdditionalDatum{
		AdditionalDatum{key: "Report", value: r.Report},
		AdditionalDatum{key: "Message", value: r.Message},
		AdditionalDatum{key: "TryAgain", value: r.TryAgain},
		AdditionalDatum{key: "URL", value: r.URL},
		AdditionalDatum{key: "Err", value: r.Err},
	}

	if r.Err != nil {
		// Failure

		i.log.Printf("Result: Installation failed")
		i.log.Printf(" Error: %+v", r.Err)

		if r.Report != "" {
			i.Report(r.Report, ScoutMeta{"err", r.Err.Error()})
		}

		if r.ShortMessage != "" {
			i.show.Println()
			i.show.Println(r.ShortMessage)
		}

		if r.Message != "" {
			i.show.Println()
			i.show.Println("AES Installation UNSUCCESSFUL")
			i.show.Println("========================================================================")
			i.show.Println()
			i.ShowTemplated(r.Message, templateData...)

			if r.URL != "" {
				i.show.Println()

				if err := browser.OpenURL(r.URL); err != nil {
					i.log.Printf("Failed to open browser: %+v", err)
				}
			}

			if r.TryAgain {
				i.show.Println()
				i.ShowWrapped("If this appears to be a transient failure, please try running the installer again. It is safe to run the installer repeatedly on a cluster.")
				i.show.Println()
			}
		}

	} else {
		// Success

		if r.Report != "" {
			i.Report(r.Report)
		}

		if r.Message != "" {
			i.show.Println()
			i.ShowTemplated(r.Message, templateData...)

			if r.URL != "" {
				i.show.Println()

				if err := browser.OpenURL(r.URL); err != nil {
					i.log.Printf("Failed to open browser: %+v", err)
				}
			}

			// Assume there's no need to show the "try again" message.
		}
	}
}
