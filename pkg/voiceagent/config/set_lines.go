package config

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// This is the config writer: it edits the source text of config.yaml line by
// line and leaves every byte it was not asked to change exactly where it was.
//
// The obvious implementation — mutate the yaml.Node tree and re-encode the
// document — cannot do that. yaml.v3 re-emits the whole file at one indent
// width, so setting a top-level key in a 4-space-nested file rewrote 25
// unrelated lines from 4-space to 2-space nesting, collapsed the gap before an
// inline comment, and dropped every blank line unless they were faked back in
// as head comments. The yaml.Node tree is still how the file is parsed,
// validated and read back; it is no longer how the file is written.

// nullTag is the tag yaml.v3 gives a key written with no value (`speaker:`).
const nullTag = "!!null"

// patchConfigSource returns source with the value at the dotted path written,
// changing only the lines that hold that key. doc must be the tree parsed from
// source and unmodified — every node's Line and Column are read as offsets into
// it.
func patchConfigSource(source []byte, doc *yaml.Node, path []string, value any) ([]byte, error) {
	if len(path) == 0 {
		return nil, fmt.Errorf("empty config key path")
	}
	src := newSourceDoc(source, doc)
	if src.root == nil || len(src.root.Content) == 0 && src.root.Line == 0 {
		return src.appendEntry(path, value)
	}
	target, err := src.locate(path)
	if err != nil {
		return nil, err
	}
	if target.keyNode != nil {
		return src.replaceEntry(target, value)
	}
	return src.insertEntry(target, value)
}

// deleteConfigKey returns source with the key at the dotted path removed,
// reporting whether the document held it at all. Only that key's own lines go;
// a comment above it belongs to whoever wrote it and is left alone.
func deleteConfigKey(source []byte, doc *yaml.Node, path []string) ([]byte, bool, error) {
	if len(path) == 0 {
		return nil, false, fmt.Errorf("empty config key path")
	}
	src := newSourceDoc(source, doc)
	target, err := src.locate(path)
	if err != nil || target.keyNode == nil || len(target.remaining) > 0 {
		return source, false, err
	}
	start, end := src.entrySpan(target.keyNode, target.valueNode)
	lines := make([]string, 0, len(src.lines))
	lines = append(lines, src.lines[:start-1]...)
	lines = append(lines, src.lines[end:]...)
	return src.join(lines), true, nil
}

// sourceDoc is config.yaml as text plus the tree parsed from it.
type sourceDoc struct {
	lines []string
	step  int        // the file's own indent width
	crlf  bool       // the file's own line ending
	root  *yaml.Node // the top-level mapping
	nodes []*yaml.Node
}

func newSourceDoc(source []byte, doc *yaml.Node) *sourceDoc {
	src := &sourceDoc{
		// Splitting on "\n" leaves a CRLF file's "\r" on the end of every line
		// it keeps, so untouched lines are untouched. The lines this writer
		// renders have to be given the same ending or the file comes back with
		// two kinds.
		lines: strings.Split(string(source), "\n"),
		crlf:  bytes.Contains(source, []byte("\r\n")),
	}
	if doc != nil && len(doc.Content) > 0 {
		src.root = doc.Content[0]
	}
	src.nodes = collectNodes(src.root, nil)
	src.step = detectIndentStep(src.root)
	return src
}

// entryTarget names where a write lands: an existing entry to rewrite
// (keyNode set), or a mapping to insert into (section set). remaining holds the
// path segments that do not exist yet and must be rendered as nested blocks.
type entryTarget struct {
	keyNode    *yaml.Node
	valueNode  *yaml.Node
	section    *yaml.Node
	sectionKey *yaml.Node
	leaf       string
	remaining  []string
}

// locate walks the path as far as the document goes.
//
// A section that exists but holds nothing — `speaker:` with no body, or
// `speaker: {}` — is reported as an entry to rewrite rather than one to insert
// into: there is no child line to anchor an insertion to, and appending under
// an explicit `null` would produce a file that no longer parses.
func (s *sourceDoc) locate(path []string) (entryTarget, error) {
	mapping, mappingKey := s.root, (*yaml.Node)(nil)
	for i, segment := range path {
		value, key := mappingEntry(mapping, segment)
		if value == nil {
			return entryTarget{section: mapping, sectionKey: mappingKey, leaf: segment, remaining: path[i+1:]}, nil
		}
		if i == len(path)-1 {
			return entryTarget{keyNode: key, valueNode: value, leaf: segment}, nil
		}
		if emptySection(value) {
			return entryTarget{keyNode: key, valueNode: value, leaf: segment, remaining: path[i+1:]}, nil
		}
		if value.Kind != yaml.MappingNode {
			return entryTarget{}, fmt.Errorf("config key %q is not a section", segment)
		}
		mapping, mappingKey = value, key
	}
	return entryTarget{}, fmt.Errorf("empty config key path")
}

// replaceEntry rewrites the lines an existing entry occupies, keeping its
// indentation and — when both the old and the new entry are a single line —
// the trailing comment and the spacing in front of it.
func (s *sourceDoc) replaceEntry(target entryTarget, value any) ([]byte, error) {
	start, end := s.entrySpan(target.keyNode, target.valueNode)
	indent := leadingSpace(s.lineAt(start))
	rendered, err := s.render(target.leaf, target.remaining, value, indent)
	if err != nil {
		return nil, err
	}
	if len(rendered) == 1 && end == start {
		rendered[0] += trailingComment(s.lineAt(start))
	}
	return s.join(splice(s.lines, start-1, end, rendered)), nil
}

// insertEntry adds a new entry to a section that already has a body, directly
// below that section's last line and at its existing children's indentation —
// never at whatever indent width the encoder would have chosen.
func (s *sourceDoc) insertEntry(target entryTarget, value any) ([]byte, error) {
	at, indent := s.insertionPoint(target.section, target.sectionKey)
	rendered, err := s.render(target.leaf, target.remaining, value, indent)
	if err != nil {
		return nil, err
	}
	return s.join(splice(s.lines, at, at, rendered)), nil
}

// appendEntry writes into a document with no mapping at all: an empty file, or
// one holding only comments.
func (s *sourceDoc) appendEntry(path []string, value any) ([]byte, error) {
	rendered, err := s.render(path[0], path[1:], value, "")
	if err != nil {
		return nil, err
	}
	at := len(s.lines)
	for at > 0 && strings.TrimSpace(s.lines[at-1]) == "" {
		at--
	}
	return s.join(splice(s.lines, at, at, rendered)), nil
}

// insertionPoint returns the index to splice at and the indentation to write,
// taken from the section's existing children so the new line looks like the
// ones around it.
func (s *sourceDoc) insertionPoint(section, sectionKey *yaml.Node) (int, string) {
	if section == nil || len(section.Content) < 2 {
		at := len(s.lines)
		for at > 0 && strings.TrimSpace(s.lines[at-1]) == "" {
			at--
		}
		return at, ""
	}
	indent := leadingSpace(s.lineAt(section.Content[0].Line))
	if sectionKey != nil && indent == leadingSpace(s.lineAt(sectionKey.Line)) {
		// A flow mapping on the section's own line has no child indentation to
		// copy; fall back to one step in from the section key.
		indent += strings.Repeat(" ", s.step)
	}
	last := len(section.Content) - 2
	_, end := s.entrySpan(section.Content[last], section.Content[last+1])
	return end, indent
}

// entrySpan returns the 1-based, inclusive line range one key/value pair
// occupies. The end is found from the next node the document holds rather than
// from the value's own extent, because a block scalar's node reports the line
// its `|` sits on and not the lines of its body.
func (s *sourceDoc) entrySpan(key, value *yaml.Node) (int, int) {
	start := key.Line
	floor := maxLine(key, maxLine(value, start))
	if value != nil && value.Kind == yaml.ScalarNode && value.Tag != nullTag {
		// A null value has no body of its own: what follows an empty `speaker:`
		// is the next key's comment block, which is not this entry's to replace.
		floor = s.blockEnd(key.Column, floor)
	}
	inside := map[*yaml.Node]bool{}
	markSubtree(key, inside)
	markSubtree(value, inside)

	end := len(s.lines)
	for _, node := range s.nodes {
		if inside[node] || node.Line <= floor {
			continue
		}
		if node.Line-1 < end {
			end = node.Line - 1
		}
	}
	for end > floor && isBlankOrComment(s.lineAt(end)) {
		end--
	}
	return start, end
}

// render is renderEntry at this document's indent width and line ending.
func (s *sourceDoc) render(key string, nested []string, value any, indent string) ([]string, error) {
	lines, err := renderEntry(key, nested, value, indent, s.step)
	if err != nil {
		return nil, err
	}
	if s.crlf {
		for i := range lines {
			lines[i] += "\r"
		}
	}
	return lines, nil
}

func (s *sourceDoc) join(lines []string) []byte {
	return []byte(strings.Join(lines, "\n"))
}

// blockEnd extends a span over the body of a multi-line scalar. yaml.v3 reports
// the line a block scalar's `|` sits on and nothing about the lines beneath it,
// so the body has to be read off the source the way YAML delimits it: every
// following line indented past the key, blank lines included. Without this the
// span stopped at the key's own line and a `config set` on such a key left the
// rest of the block stranded in the file.
func (s *sourceDoc) blockEnd(keyColumn, from int) int {
	end := from
	for line := from + 1; line <= len(s.lines); line++ {
		text := s.lines[line-1]
		if strings.TrimSpace(text) == "" {
			continue // a blank line inside a block does not end it
		}
		if len(leadingSpace(text)) < keyColumn {
			break
		}
		end = line
	}
	return end
}

func (s *sourceDoc) lineAt(line int) string {
	if line >= 1 && line <= len(s.lines) {
		return s.lines[line-1]
	}
	return ""
}

// renderEntry renders `key: value` — or the nested blocks for a path that does
// not exist yet — through the yaml encoder, so quoting, escaping and sequence
// shape stay the encoder's job, then shifts the result to the caller's indent.
func renderEntry(key string, nested []string, value any, indent string, step int) ([]string, error) {
	node, err := yamlNodeFor(value)
	if err != nil {
		return nil, err
	}
	for i := len(nested) - 1; i >= 0; i-- {
		node = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{keyNode(nested[i]), node}}
	}
	mapping := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{keyNode(key), node}}
	out, err := encodeYAMLDocumentIndent(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{mapping}}, step)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = indent + line
		}
	}
	return lines, nil
}

// detectIndentStep reads the file's own nesting width, so a new section is
// created in the style of the file it joins. Two spaces is the fallback and
// the narrowest step wins when a file mixes widths.
func detectIndentStep(root *yaml.Node) int {
	step := 0
	var walk func(mapping *yaml.Node, column int)
	walk = func(mapping *yaml.Node, column int) {
		for i := 0; i+1 < len(mapping.Content); i += 2 {
			key, value := mapping.Content[i], mapping.Content[i+1]
			if delta := key.Column - column; column > 0 && delta > 0 && (step == 0 || delta < step) {
				step = delta
			}
			if value.Kind == yaml.MappingNode {
				walk(value, key.Column)
			}
		}
	}
	if root != nil && root.Kind == yaml.MappingNode {
		walk(root, 0)
	}
	if step < 1 || step > 9 {
		return 2
	}
	return step
}

// trailingComment returns a line's comment together with the whitespace in
// front of it, so `x: 1   # why` keeps all three spaces. A `#` inside a quoted
// scalar, or one not preceded by whitespace, is not a comment.
func trailingComment(line string) string {
	var inSingle, inDouble bool
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case c == '\\' && inDouble:
			i++
		case c == '\'' && !inDouble:
			inSingle = !inSingle
		case c == '"' && !inSingle:
			inDouble = !inDouble
		case c == '#' && !inSingle && !inDouble && i > 0 && (line[i-1] == ' ' || line[i-1] == '\t'):
			return line[len(strings.TrimRight(line[:i], " \t")):]
		}
	}
	return ""
}

// mappingEntry finds a key case-insensitively — viper lowercases every key it
// reads — and returns both halves of the pair, because the key node is what
// carries the line and column the writer edits at.
func mappingEntry(mapping *yaml.Node, key string) (*yaml.Node, *yaml.Node) {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if strings.EqualFold(mapping.Content[i].Value, key) {
			return mapping.Content[i+1], mapping.Content[i]
		}
	}
	return nil, nil
}

// emptySection reports a key with nothing under it: `speaker:`, `speaker: ~`
// or `speaker: {}`.
func emptySection(node *yaml.Node) bool {
	return len(node.Content) == 0 && (node.Tag == nullTag || node.Kind == yaml.MappingNode)
}

func collectNodes(node *yaml.Node, out []*yaml.Node) []*yaml.Node {
	if node == nil {
		return out
	}
	if node.Line > 0 {
		out = append(out, node)
	}
	for _, child := range node.Content {
		out = collectNodes(child, out)
	}
	return out
}

func markSubtree(node *yaml.Node, seen map[*yaml.Node]bool) {
	if node == nil || seen[node] {
		return
	}
	seen[node] = true
	for _, child := range node.Content {
		markSubtree(child, seen)
	}
}

func maxLine(node *yaml.Node, best int) int {
	if node == nil {
		return best
	}
	if node.Line > best {
		best = node.Line
	}
	for _, child := range node.Content {
		best = maxLine(child, best)
	}
	return best
}

func leadingSpace(line string) string {
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
}

func isBlankOrComment(line string) bool {
	trimmed := strings.TrimSpace(line)
	return trimmed == "" || strings.HasPrefix(trimmed, "#")
}

// splice replaces lines[from:to] with replacement, without aliasing the input.
func splice(lines []string, from, to int, replacement []string) []string {
	out := make([]string, 0, len(lines)-(to-from)+len(replacement))
	out = append(out, lines[:from]...)
	out = append(out, replacement...)
	return append(out, lines[to:]...)
}
