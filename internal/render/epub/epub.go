// Package epub parses EPUB publications into ordered, titled chapters using only
// the standard library (archive/zip + encoding/xml). It is cgo-free and reads
// the container, OPF package (metadata/manifest/spine), and nav/NCX titles.
package epub

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
)

// Typed errors for actionable diagnostics.
var (
	ErrNoContainer = errors.New("epub: missing META-INF/container.xml")
	ErrNoPackage   = errors.New("epub: missing or unreadable OPF package document")
	ErrNoSpine     = errors.New("epub: package has no spine items")
	ErrEncrypted   = errors.New("epub: encrypted/DRM-protected EPUBs are not supported")
)

const (
	maxMetadataEntryBytes = 16 << 20
	maxChapterEntryBytes  = 64 << 20
)

// Metadata holds package-level publication metadata.
type Metadata struct {
	Title    string
	Author   string
	Language string
}

// Chapter is one spine item in reading order.
type Chapter struct {
	ID        string
	Href      string // path within the zip
	MediaType string
	Title     string // from nav/NCX when available
}

// Book is a parsed EPUB: metadata, ordered chapters, and access to chapter bytes.
type Book struct {
	Metadata Metadata
	Chapters []Chapter

	idx zipIndex
}

// Parse reads the EPUB structure from a zip.Reader. It does not read chapter
// text; use ReadChapter for that.
func Parse(zr *zip.Reader) (*Book, error) {
	// Index the archive once: chapter and metadata lookups are then O(1)
	// instead of a linear scan per lookup across every zip entry.
	idx := indexZip(zr)
	if _, err := openZipFile(idx, "META-INF/encryption.xml"); err == nil {
		return nil, ErrEncrypted
	}

	opfPath, err := containerOPFPath(idx)
	if err != nil {
		return nil, err
	}

	pkg, err := readPackage(idx, opfPath)
	if err != nil {
		return nil, err
	}
	opfDir := path.Dir(opfPath)

	// Build manifest lookup: id -> item.
	type item struct{ href, mediaType, properties string }
	byID := make(map[string]item, len(pkg.Manifest.Items))
	for _, it := range pkg.Manifest.Items {
		byID[it.ID] = item{href: it.Href, mediaType: it.MediaType, properties: it.Properties}
	}

	// Resolve spine in reading order.
	var chapters []Chapter
	for _, ref := range pkg.Spine.ItemRefs {
		if strings.EqualFold(ref.Linear, "no") {
			continue
		}
		it, ok := byID[ref.IDRef]
		if !ok {
			return nil, fmt.Errorf("epub: spine references missing manifest item %q", ref.IDRef)
		}
		chapters = append(chapters, Chapter{
			ID:        ref.IDRef,
			Href:      resolveHref(opfDir, it.href),
			MediaType: it.mediaType,
		})
	}
	if len(chapters) == 0 {
		return nil, ErrNoSpine
	}

	// Titles from nav (EPUB3) or NCX (EPUB2).
	titles := readTitles(idx, opfDir, pkg, byID2(pkg))
	for i := range chapters {
		if t := titles[chapters[i].Href]; t != "" {
			chapters[i].Title = t
		}
	}

	return &Book{
		Metadata: Metadata{
			Title:    firstNonEmpty(pkg.Metadata.Title),
			Author:   firstNonEmpty(pkg.Metadata.Creator),
			Language: firstNonEmpty(pkg.Metadata.Language),
		},
		Chapters: chapters,
		idx:      idx,
	}, nil
}

// ReadChapter returns the raw bytes of a chapter's content file.
func ReadChapter(idx zipIndex, href string) ([]byte, error) {
	f, err := openZipFile(idx, href)
	if err != nil {
		return nil, fmt.Errorf("epub: read chapter %q: %w", href, err)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("epub: open chapter %q: %w", href, err)
	}
	defer rc.Close()
	return readZipReader(rc, maxChapterEntryBytes)
}

// ReadChapter on the Book reads from its zip reader.
func (b *Book) ReadChapter(href string) ([]byte, error) { return ReadChapter(b.idx, href) }

// --- internals ---

func containerOPFPath(idx zipIndex) (string, error) {
	f, err := openZipFile(idx, "META-INF/container.xml")
	if err != nil {
		return "", ErrNoContainer
	}
	data, err := readZipFile(f)
	if err != nil {
		return "", ErrNoContainer
	}
	var c struct {
		Rootfiles struct {
			Rootfile []struct {
				FullPath string `xml:"full-path,attr"`
			} `xml:"rootfile"`
		} `xml:"rootfiles"`
	}
	if err := xml.Unmarshal(data, &c); err != nil {
		return "", fmt.Errorf("epub: parse container.xml: %w", err)
	}
	if len(c.Rootfiles.Rootfile) == 0 || c.Rootfiles.Rootfile[0].FullPath == "" {
		return "", ErrNoPackage
	}
	// full-path is a URL path per OCF, so it needs the same decoding as hrefs.
	return decodeHref(c.Rootfiles.Rootfile[0].FullPath), nil
}

type opfPackage struct {
	Metadata struct {
		Title    []string `xml:"title"`
		Creator  []string `xml:"creator"`
		Language []string `xml:"language"`
	} `xml:"metadata"`
	Manifest struct {
		Items []struct {
			ID         string `xml:"id,attr"`
			Href       string `xml:"href,attr"`
			MediaType  string `xml:"media-type,attr"`
			Properties string `xml:"properties,attr"`
		} `xml:"item"`
	} `xml:"manifest"`
	Spine struct {
		TOC      string `xml:"toc,attr"`
		ItemRefs []struct {
			IDRef  string `xml:"idref,attr"`
			Linear string `xml:"linear,attr"`
		} `xml:"itemref"`
	} `xml:"spine"`
}

func readPackage(idx zipIndex, opfPath string) (*opfPackage, error) {
	f, err := openZipFile(idx, opfPath)
	if err != nil {
		return nil, ErrNoPackage
	}
	data, err := readZipFile(f)
	if err != nil {
		return nil, ErrNoPackage
	}
	var pkg opfPackage
	if err := xml.Unmarshal(data, &pkg); err != nil {
		return nil, fmt.Errorf("epub: parse package %q: %w", opfPath, err)
	}
	return &pkg, nil
}

func byID2(pkg *opfPackage) map[string]string {
	m := make(map[string]string, len(pkg.Manifest.Items))
	for _, it := range pkg.Manifest.Items {
		m[it.ID] = it.Href
	}
	return m
}

// readTitles returns a map from chapter href to title, using the EPUB3 nav
// document or the EPUB2 NCX, whichever is available.
func readTitles(idx zipIndex, opfDir string, pkg *opfPackage, hrefByID map[string]string) map[string]string {
	// EPUB3 nav: manifest item with properties containing "nav".
	for _, it := range pkg.Manifest.Items {
		if strings.Contains(it.Properties, "nav") {
			if t := readNavTitles(idx, resolveHref(opfDir, it.Href)); len(t) > 0 {
				return t
			}
		}
	}
	// EPUB2 NCX: spine toc attr -> manifest item, or media-type.
	ncxHref := ""
	if pkg.Spine.TOC != "" {
		ncxHref = hrefByID[pkg.Spine.TOC]
	}
	if ncxHref == "" {
		for _, it := range pkg.Manifest.Items {
			if it.MediaType == "application/x-dtbncx+xml" {
				ncxHref = it.Href
				break
			}
		}
	}
	if ncxHref != "" {
		return readNCXTitles(idx, opfDir, resolveHref(opfDir, ncxHref))
	}
	return map[string]string{}
}

func readNavTitles(idx zipIndex, navHref string) map[string]string {
	data, err := readNamed(idx, navHref)
	if err != nil {
		return nil
	}
	navDir := path.Dir(navHref)
	var doc struct {
		Navs []struct {
			Links []struct {
				Href string `xml:"href,attr"`
				Text string `xml:",chardata"`
			} `xml:"ol>li>a"`
		} `xml:"body>nav"`
	}
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil
	}
	out := map[string]string{}
	for _, nav := range doc.Navs {
		for _, l := range nav.Links {
			href := stripFragment(l.Href)
			if href == "" {
				continue
			}
			out[resolveHref(navDir, href)] = strings.TrimSpace(l.Text)
		}
	}
	return out
}

func readNCXTitles(idx zipIndex, opfDir, ncxHref string) map[string]string {
	data, err := readNamed(idx, ncxHref)
	if err != nil {
		return map[string]string{}
	}
	ncxDir := path.Dir(ncxHref)
	var doc struct {
		NavPoints []ncxNavPoint `xml:"navMap>navPoint"`
	}
	if err := xml.Unmarshal(data, &doc); err != nil {
		return map[string]string{}
	}
	out := map[string]string{}
	var walk func(points []ncxNavPoint)
	walk = func(points []ncxNavPoint) {
		for _, p := range points {
			href := stripFragment(p.Content.Src)
			if href != "" {
				out[resolveHref(ncxDir, href)] = strings.TrimSpace(p.Label.Text)
			}
			walk(p.Children)
		}
	}
	walk(doc.NavPoints)
	return out
}

type ncxNavPoint struct {
	Label struct {
		Text string `xml:"text"`
	} `xml:"navLabel"`
	Content struct {
		Src string `xml:"src,attr"`
	} `xml:"content"`
	Children []ncxNavPoint `xml:"navPoint"`
}

// --- zip + path helpers ---

// zipIndex maps entry names to files for O(1) lookups.
type zipIndex map[string]*zip.File

// indexZip builds the name index; the first entry wins on duplicates, matching
// the previous linear-scan semantics.
func indexZip(zr *zip.Reader) zipIndex {
	m := make(zipIndex, len(zr.File))
	for _, f := range zr.File {
		if _, ok := m[f.Name]; !ok {
			m[f.Name] = f
		}
	}
	return m
}

func openZipFile(idx zipIndex, name string) (*zip.File, error) {
	if f, ok := idx[name]; ok {
		return f, nil
	}
	return nil, fmt.Errorf("not found: %s", name)
}

func readZipFile(f *zip.File) ([]byte, error) {
	if f.UncompressedSize64 > maxMetadataEntryBytes {
		return nil, fmt.Errorf("epub: entry %q is too large (%d bytes)", f.Name, f.UncompressedSize64)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return readZipReader(rc, maxMetadataEntryBytes)
}

func readZipReader(r io.Reader, maxBytes int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("epub: entry exceeds %d bytes", maxBytes)
	}
	return data, nil
}

func readNamed(idx zipIndex, name string) ([]byte, error) {
	f, err := openZipFile(idx, name)
	if err != nil {
		return nil, err
	}
	return readZipFile(f)
}

func resolveHref(dir, href string) string {
	href = decodeHref(stripFragment(href))
	if dir == "" || dir == "." {
		return path.Clean(href)
	}
	return path.Clean(path.Join(dir, href))
}

// decodeHref percent-decodes an OPF/nav/NCX href. Hrefs are URLs per the EPUB
// spec, so a zip entry named "chapter 1.xhtml" is referenced as
// "chapter%201.xhtml" — the raw string would never match the archive's entry
// names. Undecodable strings are used verbatim.
func decodeHref(s string) string {
	if !strings.ContainsRune(s, '%') {
		return s
	}
	decoded, err := url.PathUnescape(s)
	if err != nil {
		return s
	}
	return decoded
}

func stripFragment(s string) string {
	if i := strings.IndexByte(s, '#'); i >= 0 {
		return s[:i]
	}
	return s
}

func firstNonEmpty(ss []string) string {
	for _, s := range ss {
		if t := strings.TrimSpace(s); t != "" {
			return t
		}
	}
	return ""
}
