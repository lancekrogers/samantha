package prompts

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Load parses a samantha.prompt.v1 YAML document. Decoding is strict:
// unknown keys are validation errors.
func Load(data []byte) (*Document, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var doc Document
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("parsing prompt document: %w", err)
	}
	if err := doc.Validate(); err != nil {
		return nil, err
	}
	return &doc, nil
}

// LoadFile loads a prompt document from disk. A .yaml/.yml file must be a
// full document whose kind matches (when kind is non-empty); a .md file
// becomes an identity-only document with the caller-supplied kind and the
// file's base name.
func LoadFile(path string, kind Kind) (*Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading prompt file: %w", err)
	}
	if strings.EqualFold(filepath.Ext(path), ".md") {
		name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		return markdownDocument(name, kind, data)
	}
	doc, err := Load(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if kind != "" && doc.Prompt.Kind != kind {
		return nil, fmt.Errorf("%s: document kind %q, want %q", path, doc.Prompt.Kind, kind)
	}
	return doc, nil
}

func markdownDocument(name string, kind Kind, data []byte) (*Document, error) {
	doc := &Document{
		Schema: Schema,
		Prompt: Prompt{Name: name, Kind: kind, SystemPrompt: SystemPrompt{Identity: string(data)}},
	}
	if err := doc.Validate(); err != nil {
		return nil, err
	}
	return doc, nil
}
