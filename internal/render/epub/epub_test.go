package epub

import (
	"archive/zip"
	"bytes"
	"errors"
	"testing"
)

// buildEPUB writes a minimal EPUB into a zip and returns a reader over it.
func buildEPUB(t *testing.T, files map[string]string) *zip.Reader {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	return zr
}

const containerXML = `<?xml version="1.0"?>
<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0">
  <rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`

const contentOPF = `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0" unique-identifier="id">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>A Tiny Book</dc:title>
    <dc:creator>Jane Author</dc:creator>
    <dc:language>en</dc:language>
  </metadata>
  <manifest>
    <item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/>
    <item id="ch1" href="chap1.xhtml" media-type="application/xhtml+xml"/>
    <item id="ch2" href="chap2.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine toc="ncx">
    <itemref idref="ch1"/>
    <itemref idref="ch2"/>
  </spine>
</package>`

const tocNCX = `<?xml version="1.0"?>
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1">
  <navMap>
    <navPoint id="n1"><navLabel><text>Chapter One</text></navLabel><content src="chap1.xhtml"/></navPoint>
    <navPoint id="n2"><navLabel><text>Chapter Two</text></navLabel><content src="chap2.xhtml"/></navPoint>
  </navMap>
</ncx>`

func tinyEPUBFiles() map[string]string {
	return map[string]string{
		"META-INF/container.xml": containerXML,
		"OEBPS/content.opf":      contentOPF,
		"OEBPS/toc.ncx":          tocNCX,
		"OEBPS/chap1.xhtml":      `<html><body><h1>Chapter One</h1><p>First chapter body.</p></body></html>`,
		"OEBPS/chap2.xhtml":      `<html><body><h1>Chapter Two</h1><p>Second chapter body.</p></body></html>`,
	}
}

func TestParseEPUBMetadataSpineAndTitles(t *testing.T) {
	book, err := Parse(buildEPUB(t, tinyEPUBFiles()))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if book.Metadata.Title != "A Tiny Book" || book.Metadata.Author != "Jane Author" || book.Metadata.Language != "en" {
		t.Errorf("metadata = %+v, want title/author/language populated", book.Metadata)
	}
	if len(book.Chapters) != 2 {
		t.Fatalf("chapters = %d, want 2", len(book.Chapters))
	}
	// Spine order is authoritative.
	if book.Chapters[0].Href != "OEBPS/chap1.xhtml" || book.Chapters[1].Href != "OEBPS/chap2.xhtml" {
		t.Errorf("chapter order = [%s, %s], want chap1 then chap2", book.Chapters[0].Href, book.Chapters[1].Href)
	}
	// Titles from NCX.
	if book.Chapters[0].Title != "Chapter One" || book.Chapters[1].Title != "Chapter Two" {
		t.Errorf("titles = [%q, %q], want NCX titles", book.Chapters[0].Title, book.Chapters[1].Title)
	}
}

func TestParseEPUBReadChapter(t *testing.T) {
	zr := buildEPUB(t, tinyEPUBFiles())
	book, err := Parse(zr)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	data, err := book.ReadChapter(book.Chapters[0].Href)
	if err != nil {
		t.Fatalf("ReadChapter() error = %v", err)
	}
	if !bytes.Contains(data, []byte("First chapter body")) {
		t.Errorf("chapter content = %q, want first chapter body", data)
	}
}

func TestParseEPUBUsesNav3Titles(t *testing.T) {
	files := tinyEPUBFiles()
	// Switch to an EPUB3 nav doc instead of NCX.
	delete(files, "OEBPS/toc.ncx")
	files["OEBPS/content.opf"] = `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Nav Book</dc:title></metadata>
  <manifest>
    <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
    <item id="ch1" href="chap1.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine><itemref idref="ch1"/></spine>
</package>`
	files["OEBPS/nav.xhtml"] = `<html><body><nav epub:type="toc"><ol><li><a href="chap1.xhtml">Nav Chapter One</a></li></ol></nav></body></html>`

	book, err := Parse(buildEPUB(t, files))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(book.Chapters) != 1 || book.Chapters[0].Title != "Nav Chapter One" {
		t.Fatalf("chapters = %+v, want one chapter titled from nav", book.Chapters)
	}
}

func TestParseEPUBErrors(t *testing.T) {
	// Missing container.
	if _, err := Parse(buildEPUB(t, map[string]string{"OEBPS/content.opf": contentOPF})); !errors.Is(err, ErrNoContainer) {
		t.Errorf("missing container error = %v, want ErrNoContainer", err)
	}
	// Encrypted.
	enc := tinyEPUBFiles()
	enc["META-INF/encryption.xml"] = `<encryption/>`
	if _, err := Parse(buildEPUB(t, enc)); !errors.Is(err, ErrEncrypted) {
		t.Errorf("encrypted error = %v, want ErrEncrypted", err)
	}
	// Empty spine.
	noSpine := map[string]string{
		"META-INF/container.xml": containerXML,
		"OEBPS/content.opf":      `<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf"><metadata/><manifest/><spine/></package>`,
	}
	if _, err := Parse(buildEPUB(t, noSpine)); !errors.Is(err, ErrNoSpine) {
		t.Errorf("empty spine error = %v, want ErrNoSpine", err)
	}
	// Spine references a missing manifest item.
	badRef := map[string]string{
		"META-INF/container.xml": containerXML,
		"OEBPS/content.opf":      `<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf"><metadata/><manifest/><spine><itemref idref="ghost"/></spine></package>`,
	}
	if _, err := Parse(buildEPUB(t, badRef)); err == nil {
		t.Error("missing manifest item should error")
	}
}
