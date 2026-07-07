package narrate

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fullPlan returns a valid plan exercising every field, including the hash
// and identity fields later pipeline stages fill in.
func fullPlan() *Plan {
	return &Plan{
		Schema: SchemaVersion,
		Source: Source{
			Path:   "source/book.pdf",
			SHA256: "3a7bd3e2360a3d29eea436fcfb7e44c735d117c42d1c1835420b6b9942dd4f1b",
			Format: "pdf",
		},
		Extract: Extract{
			Method:  ExtractMethodPDFToText,
			Options: &ExtractOptions{Layout: true},
		},
		Sections: []Section{
			{
				ID:               "001-preface",
				Title:            "Preface",
				SourceRange:      &SourceRange{Pages: []int{1, 6}},
				ExtractedPath:    "extracted/001-preface.txt",
				ExtractedSHA256:  "b5bb9d8014a0f9b1d61e21e796d78dccdf1352f23cd32812f4850b878ae4944c",
				PreparedPath:     "prepared/001-preface.md",
				PreparedProvider: "claude",
				PreparedModel:    "claude-sonnet-4-5",
				AudioPath:        "audio/001-preface.wav",
			},
			{
				ID:            "002-chapter-1",
				Title:         "Chapter 1",
				SourceRange:   &SourceRange{Pages: []int{7, 42}},
				ExtractedPath: "extracted/002-chapter-1.txt",
				PreparedPath:  "prepared/002-chapter-1.md",
				AudioPath:     "audio/002-chapter-1.wav",
			},
		},
		Prompts: &Prompts{
			System:              "prompts/system.md",
			SystemSHA256:        "7d865e959b2466918c9863afca942d0fb89d7c9ac0c99bafc3749504ded97730",
			Style:               "prompts/style.md",
			StyleSHA256:         "aec070645fe53ee3b3763059376134f058cc337247c978add178b6ccdfb0019f",
			Pronunciation:       "prompts/pronunciation.md",
			PronunciationSHA256: "df3f619804a92fdb4057192dc43dd748ea778adc52bc498ce80524c014b81119",
		},
		Render: &Render{
			Voice:       "af_heart",
			Speed:       0.95,
			AudioFormat: "m4b",
		},
	}
}

func TestPlanValidate(t *testing.T) {
	tests := []struct {
		name            string
		mutate          func(p *Plan)
		wantErrContains string
	}{
		// Error cases first.
		{name: "unknown schema string", mutate: func(p *Plan) { p.Schema = "samantha.narration-plan.v2" }, wantErrContains: `schema must be "samantha.narration-plan.v1"`},
		{name: "empty schema", mutate: func(p *Plan) { p.Schema = "" }, wantErrContains: `schema must be "samantha.narration-plan.v1"`},
		{name: "missing source path", mutate: func(p *Plan) { p.Source.Path = "" }, wantErrContains: "source.path is required"},
		{name: "empty extract method", mutate: func(p *Plan) { p.Extract.Method = "" }, wantErrContains: "extract.method is required (valid: native, pdftotext)"},
		{name: "unknown extract method", mutate: func(p *Plan) { p.Extract.Method = "ocr" }, wantErrContains: `unknown extract.method "ocr" (valid: native, pdftotext)`},
		{name: "empty section id", mutate: func(p *Plan) { p.Sections[0].ID = "" }, wantErrContains: "sections[0]: id is required"},
		{name: "duplicate section ids", mutate: func(p *Plan) { p.Sections[1].ID = p.Sections[0].ID }, wantErrContains: `sections[1]: duplicate section id "001-preface"`},
		{name: "missing extracted path", mutate: func(p *Plan) { p.Sections[1].ExtractedPath = "" }, wantErrContains: "sections[1]: 002-chapter-1: extracted_path is required"},
		{name: "missing prepared path", mutate: func(p *Plan) { p.Sections[0].PreparedPath = "" }, wantErrContains: "prepared_path is required"},
		{name: "missing audio path", mutate: func(p *Plan) { p.Sections[0].AudioPath = "" }, wantErrContains: "audio_path is required"},
		{name: "pages not a pair", mutate: func(p *Plan) { p.Sections[0].SourceRange.Pages = []int{1, 6, 9} }, wantErrContains: "source_range.pages must be a [start, end] pair, got 3 value(s)"},
		{name: "pages empty", mutate: func(p *Plan) { p.Sections[0].SourceRange.Pages = nil }, wantErrContains: "source_range.pages must be a [start, end] pair, got 0 value(s)"},
		{name: "page start below one", mutate: func(p *Plan) { p.Sections[0].SourceRange.Pages = []int{0, 6} }, wantErrContains: "source_range.pages start must be >= 1, got 0"},
		{name: "page end before start", mutate: func(p *Plan) { p.Sections[0].SourceRange.Pages = []int{6, 2} }, wantErrContains: "source_range.pages end (2) must be >= start (6)"},
		{name: "negative render speed", mutate: func(p *Plan) { p.Render.Speed = -0.5 }, wantErrContains: "render.speed must be >= 0, got -0.5"},

		// Valid plans.
		{name: "full plan valid", mutate: func(p *Plan) {}},
		{name: "minimal plan valid", mutate: func(p *Plan) {
			*p = Plan{
				Schema:  SchemaVersion,
				Source:  Source{Path: "source/notes.md"},
				Extract: Extract{Method: ExtractMethodNative},
			}
		}},
		{name: "section without source range valid", mutate: func(p *Plan) { p.Sections[0].SourceRange = nil }},
		{name: "zero render speed valid", mutate: func(p *Plan) { p.Render.Speed = 0 }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := fullPlan()
			tt.mutate(p)
			err := p.Validate()
			if tt.wantErrContains != "" {
				if err == nil {
					t.Fatalf("Validate() error = nil, want containing %q", tt.wantErrContains)
				}
				if !strings.Contains(err.Error(), tt.wantErrContains) {
					t.Fatalf("Validate() error = %q, want containing %q", err, tt.wantErrContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate() error: %v", err)
			}
		})
	}
}

const specExampleDoc = `schema: samantha.narration-plan.v1
source:
  path: source/book.pdf
  sha256: 3a7bd3e2360a3d29eea436fcfb7e44c735d117c42d1c1835420b6b9942dd4f1b
  format: pdf
extract:
  method: pdftotext
  options:
    layout: false
sections:
  - id: 001-preface
    title: Preface
    source_range:
      pages: [1, 6]
    extracted_path: extracted/001-preface.txt
    prepared_path: prepared/001-preface.md
    audio_path: audio/001-preface.wav
prompts:
  system: prompts/system.md
  style: prompts/style.md
  pronunciation: prompts/pronunciation.md
render:
  voice: af_heart
  speed: 0.95
  audio_format: m4b
`

func TestParse(t *testing.T) {
	tests := []struct {
		name            string
		doc             string
		wantErrContains string
	}{
		// Error cases first.
		{name: "unknown top-level key", doc: "schema: samantha.narration-plan.v1\nnarrator: bob\n", wantErrContains: "field narrator not found"},
		{name: "unknown section key", doc: strings.Replace(specExampleDoc, "title: Preface", "bogus: 1", 1), wantErrContains: "field bogus not found"},
		{name: "unknown extract option", doc: strings.Replace(specExampleDoc, "layout: false", "columns: 2", 1), wantErrContains: "field columns not found"},
		{name: "empty document", doc: "", wantErrContains: "document is empty"},
		{name: "malformed yaml", doc: "schema: [", wantErrContains: "parsing narration plan"},
		{name: "validation applied after decode", doc: strings.Replace(specExampleDoc, "method: pdftotext", "method: ocr", 1), wantErrContains: `invalid narration plan: unknown extract.method "ocr"`},

		{name: "spec example valid", doc: specExampleDoc},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Parse([]byte(tt.doc))
			if tt.wantErrContains != "" {
				if err == nil {
					t.Fatalf("Parse() error = nil, want containing %q", tt.wantErrContains)
				}
				if !strings.Contains(err.Error(), tt.wantErrContains) {
					t.Fatalf("Parse() error = %q, want containing %q", err, tt.wantErrContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse() error: %v", err)
			}
			if len(p.Sections) != 1 {
				t.Fatalf("Parse() sections = %d, want 1", len(p.Sections))
			}
			if got := p.Sections[0].SourceRange.Pages; !reflect.DeepEqual(got, []int{1, 6}) {
				t.Errorf("Parse() pages = %v, want [1 6]", got)
			}
			if p.Render == nil || p.Render.Speed != 0.95 {
				t.Errorf("Parse() render = %+v, want speed 0.95", p.Render)
			}
		})
	}
}

func TestLoadSaveRoundTrip(t *testing.T) {
	want := fullPlan()
	path := filepath.Join(t.TempDir(), "plans", "book.yaml")
	if err := want.Save(path); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch:\ngot:  %+v\nwant: %+v", got, want)
	}
}

func TestSaveRejectsInvalidPlan(t *testing.T) {
	p := fullPlan()
	p.Sections[1].ID = p.Sections[0].ID
	path := filepath.Join(t.TempDir(), "book.yaml")
	err := p.Save(path)
	if err == nil {
		t.Fatal("Save() error = nil, want duplicate section id error")
	}
	if !strings.Contains(err.Error(), "duplicate section id") {
		t.Fatalf("Save() error = %q, want containing %q", err, "duplicate section id")
	}
}
