package rag

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"html"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
	htmlparser "golang.org/x/net/html"
)

const LegacyChunkVersion = "rune-800-overlap-120-v1"
const MarkdownChunkVersion = "markdown-block-800-v1"

// ImageReference describes source syntax only. No URL is fetched by parsing.
// Definition spans describe the separate reference definition, never the use.
type ImageReference struct {
	ReferenceLabel      string `json:"reference_label,omitempty"`
	URL                 string `json:"url"`
	Alt                 string `json:"alt"`
	Kind                string `json:"kind"`
	Status              string `json:"status"`
	StartByte           int    `json:"start_byte"`
	EndByte             int    `json:"end_byte"`
	StartLine           int    `json:"start_line"`
	EndLine             int    `json:"end_line"`
	DefinitionStartByte *int   `json:"definition_start_byte,omitempty"`
	DefinitionEndByte   *int   `json:"definition_end_byte,omitempty"`
}

// BlockSpan preserves the actual AST block kinds inside a grouped chunk.
type BlockSpan struct {
	Type      string `json:"type"`
	StartByte int    `json:"start_byte"`
	EndByte   int    `json:"end_byte"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}
type MarkdownBlock struct {
	node             ast.Node
	Type             string           `json:"type"`
	HeadingPath      []string         `json:"heading_path"`
	StartByte        int              `json:"start_byte"`
	EndByte          int              `json:"end_byte"`
	StartLine        int              `json:"start_line"`
	EndLine          int              `json:"end_line"`
	EmbeddingContent string           `json:"embedding_content"`
	ImageRefs        []ImageReference `json:"image_refs"`
}
type MarkdownDocument struct {
	Blocks    []MarkdownBlock  `json:"blocks"`
	ImageRefs []ImageReference `json:"image_refs"`
}

const sourceStartAttribute = "learnq_source_start"
const imageOpenAttribute = "learnq_image_open"
const imageEndAttribute = "learnq_image_end"
const imageKindAttribute = "learnq_image_kind"
const imageLabelAttribute = "learnq_image_label"

// Goldmark's block lines omit syntax markers. Capture the physical source line
// at block opening, then partition the original bytes at top-level boundaries.
// This also preserves blank lines and reference definitions removed from the AST.
type sourceBlockParser struct{ parser.BlockParser }

func (p sourceBlockParser) Open(parent ast.Node, r text.Reader, pc parser.Context) (ast.Node, parser.State) {
	_, seg := r.PeekLine()
	n, state := p.BlockParser.Open(parent, r, pc)
	if n != nil {
		n.SetAttributeString(sourceStartAttribute, lineStart(r.Source(), seg.Start))
	}
	return n, state
}

type referenceSource struct {
	label      string
	start, end int
}
type sourceReferenceTransformer struct{ definitions *[]referenceSource }

func (p sourceReferenceTransformer) Transform(n *ast.Paragraph, r text.Reader, pc parser.Context) {
	parent, previous := n.Parent(), n.PreviousSibling()
	start, _ := n.AttributeString(sourceStartAttribute)
	original := make([]text.Segment, n.Lines().Len())
	for j := range original {
		original[j] = n.Lines().At(j)
	}
	before := make(map[string]bool)
	for _, ref := range pc.References() {
		before[util.ToLinkReference(ref.Label())] = true
	}
	parser.LinkReferenceParagraphTransformer.Transform(n, r, pc)
	remaining := make(map[int]bool)
	if n.Parent() != nil {
		for j := 0; j < n.Lines().Len(); j++ {
			remaining[n.Lines().At(j).Start] = true
		}
	}
	added := make(map[string]bool)
	for _, ref := range pc.References() {
		label := util.ToLinkReference(ref.Label())
		if !before[label] {
			added[label] = true
		}
	}
	type definitionStart struct {
		label  string
		offset int
	}
	var starts []definitionStart
	removedEnd := 0
	for _, segment := range original {
		if remaining[segment.Start] {
			continue
		}
		removedEnd = max(removedEnd, segment.Stop)
		value := segment.Value(r.Source())
		matches := referenceDefinition.FindSubmatchIndex(value)
		if len(matches) > 0 {
			label := util.ToLinkReference(value[matches[2]:matches[3]])
			starts = append(starts, definitionStart{label, lineStart(r.Source(), segment.Start)})
		}
	}
	for j, definition := range starts {
		if !added[definition.label] {
			continue
		}
		end := removedEnd
		if j+1 < len(starts) {
			end = starts[j+1].offset
		}
		*p.definitions = append(*p.definitions, referenceSource{definition.label, definition.offset, end})
		delete(added, definition.label)
	}
	if n.Parent() == nil && parent != nil {
		replacement := parent.FirstChild()
		if previous != nil {
			replacement = previous.NextSibling()
		}
		if replacement != nil && start != nil {
			replacement.SetAttributeString(sourceStartAttribute, start)
		}
	}
}

// The link parser records syntax positions while Goldmark resolves links and
// references. Locating images by substring search would confuse repeated text.
type sourceLinkParser struct {
	parser.InlineParser
	unresolved []ImageReference
}

func (p *sourceLinkParser) Parse(parent ast.Node, r text.Reader, pc parser.Context) ast.Node {
	line, seg := r.PeekLine()
	if len(line) == 0 {
		return nil
	}
	trigger := line[0]
	start := -1
	isImage := false
	if trigger == ']' {
		for child := parent.LastChild(); child != nil; child = child.PreviousSibling() {
			if value, ok := child.AttributeString(sourceStartAttribute); ok {
				start = value.(int)
				value, _ = child.AttributeString(imageOpenAttribute)
				isImage, _ = value.(bool)
				break
			}
		}
	}
	n := p.InlineParser.Parse(parent, r, pc)
	if n != nil && (trigger == '!' || trigger == '[') {
		n.SetAttributeString(sourceStartAttribute, seg.Start)
		n.SetAttributeString(imageOpenAttribute, trigger == '!')
	}
	if _, ok := n.(*ast.Image); ok && start >= 0 {
		_, end := r.Position()
		n.SetAttributeString(sourceStartAttribute, start)
		n.SetAttributeString(imageEndAttribute, end.Start)
		kind, label := "reference", string(r.Source()[start+2:seg.Start])
		tail := r.Source()[seg.Start+1 : end.Start]
		if len(tail) > 0 && tail[0] == '(' {
			kind = "inline"
		} else if len(tail) > 2 && tail[0] == '[' && tail[len(tail)-1] == ']' {
			label = string(tail[1 : len(tail)-1])
		}
		n.SetAttributeString(imageKindAttribute, kind)
		n.SetAttributeString(imageLabelAttribute, util.ToLinkReference([]byte(label)))
	}
	if n == nil && trigger == ']' && start >= 0 && isImage {
		// Only unresolved reference syntax is retained; malformed destinations are text.
		end := seg.Start + 1
		source := r.Source()
		if end < len(source) && source[end] == '[' {
			if close := bytes.IndexByte(source[end+1:], ']'); close >= 0 && close < 1000 {
				end += close + 2
			}
		}
		if end <= len(source) && (end == len(source) || source[seg.Start+1] != '(') {
			p.unresolved = append(p.unresolved, ImageReference{Alt: string(source[start+2 : seg.Start]), Kind: "reference", Status: "unresolved_reference", StartByte: start, EndByte: end})
		}
	}
	return n
}
func (p *sourceLinkParser) CloseBlock(parent ast.Node, r text.Reader, pc parser.Context) {
	if closer, ok := p.InlineParser.(parser.CloseBlocker); ok {
		closer.CloseBlock(parent, r, pc)
	}
}

func ParseMarkdown(source string) (MarkdownDocument, error) {
	if !utf8.ValidString(source) {
		return MarkdownDocument{}, errors.New("Markdown source must be valid UTF-8")
	}
	if source == "" {
		return MarkdownDocument{}, nil
	}
	raw := []byte(source)
	blocks := parser.DefaultBlockParsers()
	for j := range blocks {
		blocks[j].Value = sourceBlockParser{blocks[j].Value.(parser.BlockParser)}
	}
	links := &sourceLinkParser{InlineParser: parser.NewLinkParser()}
	inlines := parser.DefaultInlineParsers()
	inlines[1].Value = links
	var definitions []referenceSource
	p := parser.NewParser(parser.WithBlockParsers(blocks...), parser.WithInlineParsers(inlines...), parser.WithParagraphTransformers(util.Prioritized(sourceReferenceTransformer{definitions: &definitions}, 100)))
	pc := parser.NewContext()
	doc := p.Parse(text.NewReader(raw), parser.WithContext(pc))
	out := MarkdownDocument{}
	nodes := []ast.Node{}
	starts := []int{}
	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		value, ok := n.AttributeString(sourceStartAttribute)
		if !ok {
			continue
		}
		start := value.(int)
		if len(starts) > 0 && start <= starts[len(starts)-1] {
			continue
		}
		nodes = append(nodes, n)
		starts = append(starts, start)
	}
	if len(nodes) == 0 {
		out.Blocks = []MarkdownBlock{{Type: "source", StartByte: 0, EndByte: len(raw), StartLine: 1, EndLine: sourceLine(raw, len(raw)-1), EmbeddingContent: ""}}
		return out, nil
	}
	headings := make([]string, 6)
	for j, n := range nodes {
		start := starts[j]
		if j == 0 {
			start = 0
		}
		end := len(raw)
		if j+1 < len(starts) {
			end = starts[j+1]
		}
		if heading, ok := n.(*ast.Heading); ok {
			headings[heading.Level-1] = plainNode(n, raw)
			for k := heading.Level; k < len(headings); k++ {
				headings[k] = ""
			}
		}
		path := []string{}
		for _, heading := range headings {
			if heading != "" {
				path = append(path, heading)
			}
		}
		block := MarkdownBlock{node: n, Type: blockType(n), HeadingPath: path, StartByte: start, EndByte: end, StartLine: sourceLine(raw, start), EndLine: sourceLine(raw, end-1), EmbeddingContent: plainNode(n, raw)}
		_ = ast.Walk(n, func(child ast.Node, enter bool) (ast.WalkStatus, error) {
			if !enter {
				return ast.WalkContinue, nil
			}
			if rawHTML, ok := child.(*ast.RawHTML); ok && rawHTML.Segments.Len() > 0 {
				first, last := rawHTML.Segments.At(0), rawHTML.Segments.At(rawHTML.Segments.Len()-1)
				refs := htmlImageReferences(raw, first.Start, last.Stop)
				block.ImageRefs = append(block.ImageRefs, refs...)
				out.ImageRefs = append(out.ImageRefs, refs...)
			}
			image, ok := child.(*ast.Image)
			if !ok {
				return ast.WalkContinue, nil
			}
			a, aok := image.AttributeString(sourceStartAttribute)
			b, bok := image.AttributeString(imageEndAttribute)
			if !aok || !bok {
				return ast.WalkContinue, nil
			}
			ref := ImageReference{URL: html.UnescapeString(string(image.Destination)), Alt: plainNode(image, raw), Kind: "inline", Status: "pending_snapshot", StartByte: a.(int), EndByte: b.(int)}
			kind, _ := image.AttributeString(imageKindAttribute)
			ref.Kind, _ = kind.(string)
			label, _ := image.AttributeString(imageLabelAttribute)
			ref.ReferenceLabel, _ = label.(string)
			ref.StartLine = sourceLine(raw, ref.StartByte)
			ref.EndLine = sourceLine(raw, ref.EndByte-1)
			block.ImageRefs = append(block.ImageRefs, ref)
			out.ImageRefs = append(out.ImageRefs, ref)
			return ast.WalkContinue, nil
		})
		if block.Type == "html" {
			refs := htmlImageReferences(raw, start, end)
			block.ImageRefs = append(block.ImageRefs, refs...)
			out.ImageRefs = append(out.ImageRefs, refs...)
		}
		out.Blocks = append(out.Blocks, block)
	}
	for _, ref := range links.unresolved {
		ref.StartLine = sourceLine(raw, ref.StartByte)
		ref.EndLine = sourceLine(raw, ref.EndByte-1)
		out.ImageRefs = append(out.ImageRefs, ref)
	}
	// Resolve definition positions after all code ranges are known, so text inside
	// fenced/indented code can never masquerade as a reference definition.
	for j := range out.ImageRefs {
		ref := &out.ImageRefs[j]
		if ref.Kind == "reference" && ref.Status != "unresolved_reference" {
			ref.DefinitionStartByte = nil
			ref.DefinitionEndByte = nil
			if a, b, ok := definitionSpan(ref.ReferenceLabel, definitions); ok {
				ref.DefinitionStartByte = &a
				ref.DefinitionEndByte = &b
			}
		}
	}
	sort.SliceStable(out.ImageRefs, func(a, b int) bool { return out.ImageRefs[a].StartByte < out.ImageRefs[b].StartByte })
	for j := range out.Blocks {
		block := &out.Blocks[j]
		block.ImageRefs = nil
		for _, ref := range out.ImageRefs {
			if ref.StartByte >= block.StartByte && ref.StartByte < block.EndByte {
				block.ImageRefs = append(block.ImageRefs, ref)
			}
		}
	}
	return out, nil
}
func blockType(n ast.Node) string {
	switch n.(type) {
	case *ast.Heading:
		return "heading"
	case *ast.Paragraph:
		return "paragraph"
	case *ast.List:
		return "list"
	case *ast.Blockquote:
		return "blockquote"
	case *ast.FencedCodeBlock, *ast.CodeBlock:
		return "code"
	case *ast.HTMLBlock:
		return "html"
	case *ast.ThematicBreak:
		return "thematic_break"
	default:
		return "source"
	}
}
func plainNode(n ast.Node, source []byte) string {
	var b strings.Builder
	_ = ast.Walk(n, func(child ast.Node, enter bool) (ast.WalkStatus, error) {
		if !enter {
			switch child.(type) {
			case *ast.Paragraph, *ast.TextBlock, *ast.ListItem:
				b.WriteByte('\n')
			}
			return ast.WalkContinue, nil
		}
		switch value := child.(type) {
		case *ast.Text:
			b.Write(value.Segment.Value(source))
			if value.SoftLineBreak() || value.HardLineBreak() {
				b.WriteByte('\n')
			}
		case *ast.String:
			b.Write(value.Value)
		case *ast.AutoLink:
			b.Write(value.Label(source))
		case *ast.FencedCodeBlock, *ast.CodeBlock:
			lines := child.Lines()
			for j := 0; j < lines.Len(); j++ {
				seg := lines.At(j)
				b.Write(seg.Value(source))
			}
			return ast.WalkSkipChildren, nil
		case *ast.RawHTML, *ast.HTMLBlock:
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	return strings.TrimSpace(html.UnescapeString(b.String()))
}
func lineStart(source []byte, offset int) int {
	if offset < 0 {
		return 0
	}
	if offset > len(source) {
		offset = len(source)
	}
	return bytes.LastIndexByte(source[:offset], '\n') + 1
}
func sourceLine(source []byte, offset int) int {
	if offset < 0 {
		return 1
	}
	if offset > len(source) {
		offset = len(source)
	}
	return 1 + bytes.Count(source[:offset], []byte{'\n'})
}

var referenceDefinition = regexp.MustCompile(`(?m)^ {0,3}\[([^\]\n]+)\]:`)

func definitionSpan(label string, definitions []referenceSource) (int, int, bool) {
	for _, definition := range definitions {
		if definition.label == label {
			return definition.start, definition.end, true
		}
	}
	return 0, 0, false
}

// ChunkMarkdown groups adjacent structural blocks under one heading. Content
// remains a contiguous original-byte slice; clean text has its own representation.
func ChunkMarkdown(source string) ([]Chunk, error) {
	parsed, err := ParseMarkdown(source)
	if err != nil {
		return nil, err
	}
	raw := []byte(source)
	var out []Chunk
	for j := 0; j < len(parsed.Blocks); {
		block := parsed.Blocks[j]
		start, end := block.StartByte, block.EndByte
		kind := block.Type
		clean := block.EmbeddingContent
		refs := append([]ImageReference(nil), block.ImageRefs...)
		spans := []BlockSpan{{block.Type, block.StartByte, block.EndByte, block.StartLine, block.EndLine}}
		next := j + 1
		for next < len(parsed.Blocks) {
			candidate := parsed.Blocks[next]
			if candidate.Type == "heading" || strings.Join(candidate.HeadingPath, "\x00") != strings.Join(block.HeadingPath, "\x00") || utf8.RuneCount(raw[start:candidate.EndByte]) > DefaultChunkSize {
				break
			}
			end = candidate.EndByte
			clean += "\n\n" + candidate.EmbeddingContent
			refs = append(refs, candidate.ImageRefs...)
			spans = append(spans, BlockSpan{candidate.Type, candidate.StartByte, candidate.EndByte, candidate.StartLine, candidate.EndLine})
			if kind != candidate.Type {
				kind = "mixed"
			}
			next++
		}
		if utf8.RuneCount(raw[start:end]) <= DefaultChunkSize {
			out = append(out, makeMarkdownChunk(raw, start, end, kind, block.HeadingPath, strings.TrimSpace(clean), refs, spans, len(out)))
		} else {
			// Oversized blocks are split at exact UTF-8 boundaries. Clean each source
			// fragment independently, retaining code/HTML semantics without invented offsets.
			for offset := start; offset < end; {
				stop := offset
				for count := 0; stop < end && count < DefaultChunkSize; count++ {
					_, size := utf8.DecodeRune(raw[stop:end])
					stop += size
				}
				embedding := ""
				if block.node != nil {
					embedding = plainNodeRange(block.node, raw, offset, stop)
				}
				var selected []ImageReference
				for _, ref := range refs {
					if ref.StartByte >= offset && ref.StartByte < stop {
						selected = append(selected, ref)
					}
				}
				out = append(out, makeMarkdownChunk(raw, offset, stop, kind, block.HeadingPath, embedding, selected, spans, len(out)))
				offset = stop
			}
		}
		j = next
	}
	return out, nil
}
func makeMarkdownChunk(source []byte, start, end int, kind string, path []string, clean string, refs []ImageReference, spans []BlockSpan, index int) Chunk {
	content := string(source[start:end])
	sum := sha256.Sum256([]byte(content))
	hash := hex.EncodeToString(sum[:])
	title := ""
	if len(path) > 0 {
		title = path[len(path)-1]
	}
	return Chunk{ID: hash, Hash: hash, Index: index, Title: title, StartByte: start, EndByte: end, StartLine: sourceLine(source, start), EndLine: sourceLine(source, end-1), Content: content, EmbeddingContent: clean, HeadingPath: append([]string(nil), path...), BlockType: kind, ImageRefs: refs, BlockSpans: spans}
}

// HTML images are inventoried explicitly as unsupported image evidence. Parsing
// their syntax does not imply the Markdown image pipeline can process them.
func htmlImageReferences(source []byte, start, end int) []ImageReference {
	var refs []ImageReference
	tokenizer := htmlparser.NewTokenizer(bytes.NewReader(source[start:end]))
	offset := start
	for {
		kind := tokenizer.Next()
		raw := tokenizer.Raw()
		next := offset + len(raw)
		if kind == htmlparser.ErrorToken {
			break
		}
		if kind == htmlparser.StartTagToken || kind == htmlparser.SelfClosingTagToken {
			token := tokenizer.Token()
			if token.Data == "img" {
				ref := ImageReference{Kind: "html", Status: "unsupported_html_image", StartByte: offset, EndByte: next, StartLine: sourceLine(source, offset), EndLine: sourceLine(source, next-1)}
				for _, attr := range token.Attr {
					if attr.Key == "src" {
						ref.URL = attr.Val
					}
					if attr.Key == "alt" {
						ref.Alt = attr.Val
					}
				}
				refs = append(refs, ref)
			}
		}
		offset = next
	}
	return refs
}

// Clip actual AST text segments to a raw-byte window. Re-parsing a truncated
// image/link/HTML token could turn its URL or markup tail into apparent prose.
func plainNodeRange(n ast.Node, source []byte, start, end int) string {
	var b strings.Builder
	writeSegment := func(segment text.Segment) {
		a, z := max(start, segment.Start), min(end, segment.Stop)
		if a < z {
			b.Write(source[a:z])
		}
	}
	_ = ast.Walk(n, func(child ast.Node, enter bool) (ast.WalkStatus, error) {
		if !enter {
			switch child.(type) {
			case *ast.Paragraph, *ast.TextBlock, *ast.ListItem:
				b.WriteByte('\n')
			}
			return ast.WalkContinue, nil
		}
		switch value := child.(type) {
		case *ast.Text:
			writeSegment(value.Segment)
			if value.Segment.Start < end && value.Segment.Stop > start && (value.SoftLineBreak() || value.HardLineBreak()) {
				b.WriteByte('\n')
			}
		case *ast.AutoLink: // Autolinks are meaningful prose URLs, unlike link destinations.
			b.Write(value.Label(source))
		case *ast.FencedCodeBlock, *ast.CodeBlock:
			for j := 0; j < child.Lines().Len(); j++ {
				writeSegment(child.Lines().At(j))
			}
			return ast.WalkSkipChildren, nil
		case *ast.RawHTML, *ast.HTMLBlock:
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	return strings.TrimSpace(html.UnescapeString(b.String()))
}
