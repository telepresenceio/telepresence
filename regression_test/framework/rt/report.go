package rt

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"os"
	"path/filepath"
)

// writeManifest serializes the run's Manifest to ArtifactDir()/manifest.json.
func (r *Runtime) writeManifest() error {
	m := r.manifest.snapshot()
	data, err := json.Marshal(m, jsontext.WithIndent("  "))
	if err != nil {
		return err
	}
	path := filepath.Join(r.ArtifactDir(), "manifest.json")
	return os.WriteFile(path, data, 0o644)
}

// printSummary logs pass/fail/skip counts for the run.
func (r *Runtime) printSummary() {
	m := r.manifest.snapshot()
	var pass, fail, skip int
	for _, t := range m.Tests {
		switch t.Outcome {
		case "pass":
			pass++
		case "fail":
			fail++
		case "skip":
			skip++
		}
	}
	r.Infof("[rtest] run %s: %d passed, %d failed, %d skipped (%d fixture actions)",
		r.runID, pass, fail, skip, len(m.Fixtures))
}
