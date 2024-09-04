package relnotes

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"io"
	"os"
	"text/template"
	"time"

	"sigs.k8s.io/yaml"
)

//go:embed relnotes.gomd
var relnotesData []byte

const templateName = "relnotes.gomd"

type NoteType string

const (
	NoteTypeFeature  NoteType = "feature"
	NoteTypeBugFix   NoteType = "bugfix"
	NoteTypeSecurity NoteType = "security"
	NoteTypeChange   NoteType = "change"
)

type Note struct {
	Type  NoteType `json:"type,omitempty"`
	Title string   `json:"title,omitempty"`
	Body  string   `json:"body,omitempty"`
	Image string   `json:"image,omitempty"`
	Docs  string   `json:"docs,omitempty"`
	HRef  string   `json:"href,omitempty"`
}

type Release struct {
	Version    string     `json:"version,omitempty"`
	DateString string     `json:"date,omitempty"`
	Notes      []Note     `json:"notes,omitempty"`
	Date       *time.Time `json:"-"`
}

type ChangeLog struct {
	Styles         ReleaseStyles `json:"-"`
	DocTitle       string        `json:"docTitle,omitempty"`
	DocDescription string        `json:"docDescription,omitempty"`
	Items          []Release     `json:"items,omitempty"`
}

type ReleaseStyles struct {
	Main string
	Date string
	Note NoteStyles
}

type NoteStyles struct {
	Main        string
	Description string // contains icon and description
	Icon        string
	Title       string
	TitleNoLink string
	Body        string
	Image       string
}

func MakeReleaseNotes(input string) error {
	cl, err := readChangeLog(input)
	if err != nil {
		return err
	}
	cl.Styles = ReleaseStyles{
		Main: "release",
		Date: "release__date",
		Note: NoteStyles{
			Main:        "note",
			Description: "note__description",
			Icon:        "note__typeIcon",
			Title:       "note__title",
			TitleNoLink: "note__title_no_link",
			Body:        "note__body",
			Image:       "note__image",
		},
	}
	wr := bufio.NewWriter(os.Stdout)
	if err := applyTemplate(cl, wr); err != nil {
		return err
	}
	return wr.Flush()
}

func (t *Release) UnmarshalJSON(data []byte) error {
	type Alias Release
	err := json.Unmarshal(data, (*Alias)(t))
	if err == nil && t.DateString != "" {
		if ts, tsErr := time.Parse("2006-01-02", t.DateString); tsErr == nil {
			t.Date = &ts
		}
	}
	return err
}

func readChangeLog(input string) (*ChangeLog, error) {
	data, err := os.ReadFile(input)
	if err != nil {
		return nil, err
	}
	var changeLog ChangeLog
	err = yaml.Unmarshal(data, &changeLog)
	if err != nil {
		return nil, err
	}
	return &changeLog, nil
}

func applyTemplate(data any, wr io.Writer) error {
	tpl, err := template.New(templateName).Parse(string(relnotesData))
	if err == nil {
		err = tpl.ExecuteTemplate(wr, templateName, data)
	}
	return err
}
