package config

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tarEntry describes one entry to write into a test tar archive.
type tarEntry struct {
	name     string
	typeflag byte
	body     string
	linkname string
}

func makeTar(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Typeflag: e.typeflag, Mode: 0o644, Linkname: e.linkname}
		if e.typeflag == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		if e.typeflag == tar.TypeDir {
			hdr.Mode = 0o755
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %q: %v", e.name, err)
		}
		if e.typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatalf("write body %q: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return buf.Bytes()
}

func extract(t *testing.T, entries []tarEntry, dir, name string, checkFiles []string) error {
	t.Helper()
	_, err := extractTarStream(bytes.NewReader(makeTar(t, entries)), dir, name, checkFiles)
	return err
}

func TestExtractTarStreamDirectoriesAndNestedFiles(t *testing.T) {
	dir := t.TempDir()
	entries := []tarEntry{
		{name: "model/", typeflag: tar.TypeDir},
		{name: "model/encoder.onnx", typeflag: tar.TypeReg, body: "enc"},
		{name: "model/sub/", typeflag: tar.TypeDir},
		{name: "model/sub/tokens.txt", typeflag: tar.TypeReg, body: "tok"},
	}
	// The "model/" top-level prefix is stripped, so files land directly in dir.
	if err := extract(t, entries, dir, "test", []string{"encoder.onnx", "sub/tokens.txt"}); err != nil {
		t.Fatalf("extract error = %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "encoder.onnx")); string(got) != "enc" {
		t.Errorf("encoder.onnx = %q, want enc", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "sub", "tokens.txt")); string(got) != "tok" {
		t.Errorf("sub/tokens.txt = %q, want tok", got)
	}
	// No leftover temp extraction dir.
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".extract-") {
			t.Errorf("leftover temp dir %q", e.Name())
		}
	}
}

func TestExtractTarStreamRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	entries := []tarEntry{
		{name: "model/", typeflag: tar.TypeDir},
		{name: "model/../../escape.txt", typeflag: tar.TypeReg, body: "evil"},
	}
	err := extract(t, entries, dir, "test", nil)
	if err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("error = %v, want an unsafe-path rejection", err)
	}
	// The escape file must not exist anywhere near dir's parent.
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(dir), "escape.txt")); !os.IsNotExist(statErr) {
		t.Error("traversal entry escaped the extraction root")
	}
}

func TestExtractTarStreamRejectsAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	entries := []tarEntry{{name: "/etc/evil.txt", typeflag: tar.TypeReg, body: "evil"}}
	if err := extract(t, entries, dir, "test", nil); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("error = %v, want an absolute-path rejection", err)
	}
}

func TestExtractTarStreamRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	entries := []tarEntry{
		{name: "model/", typeflag: tar.TypeDir},
		{name: "model/link", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"},
	}
	if err := extract(t, entries, dir, "test", nil); err == nil || !strings.Contains(err.Error(), "link") {
		t.Fatalf("error = %v, want a link-entry rejection", err)
	}
}

func TestExtractTarStreamMissingCheckFilesFails(t *testing.T) {
	dir := t.TempDir()
	entries := []tarEntry{
		{name: "model/", typeflag: tar.TypeDir},
		{name: "model/only.txt", typeflag: tar.TypeReg, body: "x"},
	}
	err := extract(t, entries, dir, "test", []string{"missing.onnx"})
	if err == nil || !strings.Contains(err.Error(), "missing expected files") {
		t.Fatalf("error = %v, want a missing-check-files failure", err)
	}
	// On failure nothing is promoted into dir.
	if _, statErr := os.Stat(filepath.Join(dir, "only.txt")); !os.IsNotExist(statErr) {
		t.Error("files were promoted despite a check-file failure")
	}
}

func TestExtractTarStreamPartialRerunIsClean(t *testing.T) {
	dir := t.TempDir()
	entries := []tarEntry{
		{name: "model/", typeflag: tar.TypeDir},
		{name: "model/model.onnx", typeflag: tar.TypeReg, body: "v1"},
	}
	if err := extract(t, entries, dir, "test", []string{"model.onnx"}); err != nil {
		t.Fatalf("first extract error = %v", err)
	}
	// Re-run with updated content; promotion must replace cleanly.
	entries[1].body = "v2"
	if err := extract(t, entries, dir, "test", []string{"model.onnx"}); err != nil {
		t.Fatalf("rerun extract error = %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "model.onnx")); string(got) != "v2" {
		t.Errorf("after rerun model.onnx = %q, want v2", got)
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".extract-") {
			t.Errorf("leftover temp dir %q after rerun", e.Name())
		}
	}
}
