package rag

import (
	"crypto/sha256"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMarkdownChunksKeepRawPositionsAndDuplicateIdentity(t *testing.T) {
	base := "# 总论\n\n## 相同标题\n\n**原文** 机制段落。\n\n- 第一步\n- 第二步\n\n## 相同标题\n\n**原文** 机制段落。\n\n```go\n# 不是标题\nfmt.Println(\"保留代码\")\n```\n\n> 引用依据\n\n尾部结论。\n"
	for _, newline := range []string{"LF", "CRLF"} {
		t.Run(newline, func(t *testing.T) {
			source := base
			if newline == "CRLF" {
				source = strings.ReplaceAll(source, "\n", "\r\n")
			}
			chunks, err := ChunkMarkdown(source)
			if err != nil {
				t.Fatal(err)
			}
			again, err := ChunkMarkdown(source)
			if err != nil || !reflect.DeepEqual(chunks, again) {
				t.Fatal("structure chunking is nondeterministic")
			}
			if len(chunks) < 3 {
				t.Fatalf("structure lost headings: %#v", chunks)
			}
			covered := make([]bool, len(source))
			for _, c := range chunks {
				for i := c.StartByte; i < c.EndByte; i++ {
					covered[i] = true
				}
				assertMarkdownRawSpan(t, source, c)
				if c.BlockType == "" {
					t.Fatal("missing structural type")
				}
				for _, heading := range c.HeadingPath {
					if strings.Contains(heading, "不是标题") {
						t.Fatal("code fence content became heading")
					}
				}
			}
			for i, present := range covered {
				if !present {
					t.Fatalf("original byte %d omitted; cross-block spans would be incomplete", i)
				}
			}
		})
	}
}

func assertMarkdownRawSpan(t *testing.T, source string, c Chunk) {
	t.Helper()
	if c.StartByte < 0 || c.EndByte <= c.StartByte || c.EndByte > len(source) || source[c.StartByte:c.EndByte] != c.Content {
		t.Fatalf("invalid raw span: %#v", c)
	}
	if !utf8.ValidString(c.Content) {
		t.Fatal("split UTF-8")
	}
	if c.Hash != fmt.Sprintf("%x", sha256.Sum256([]byte(c.Content))) {
		t.Fatal("chunk hash is not original raw content")
	}
	if c.StartLine != 1+strings.Count(source[:c.StartByte], "\n") || c.EndLine != 1+strings.Count(source[:c.EndByte-1], "\n") {
		t.Fatalf("raw lines inconsistent: %#v", c)
	}
}

func TestMarkdownLongChineseParagraphDoesNotLoseTail(t *testing.T) {
	source := "# 长行\r\n\r\n" + strings.Repeat("中文🙂机制", 500) + "末尾独有结论\r\n"
	chunks, err := ChunkMarkdown(source)
	if err != nil {
		t.Fatal(err)
	}
	covered := make([]bool, len(source))
	for _, c := range chunks {
		assertMarkdownRawSpan(t, source, c)
		for i := c.StartByte; i < c.EndByte; i++ {
			covered[i] = true
		}
	}
	start := strings.Index(source, "中文")
	for i := start; i < len(source); i++ {
		if !covered[i] {
			t.Fatalf("lost paragraph byte %d", i)
		}
	}
	if len(chunks) < 3 {
		t.Fatal("long paragraph was not bounded")
	}
}

func TestMarkdownImagesKeepUseAndDefinitionSpans(t *testing.T) {
	source := "# 图片\r\n\r\n![行内说明](https://example.test/a.png)\r\n\r\n![引用说明][flow]\r\n\r\n[flow]: https://example.test/b.png \"说明\"\r\n"
	parsed, err := ParseMarkdown(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.ImageRefs) != 2 {
		t.Fatalf("image refs=%#v", parsed.ImageRefs)
	}
	urls := map[string]bool{}
	for _, ref := range parsed.ImageRefs {
		urls[ref.URL] = true
		if ref.StartByte < 0 || ref.EndByte <= ref.StartByte || ref.EndByte > len(source) || !strings.Contains(source[ref.StartByte:ref.EndByte], "![") {
			t.Fatalf("image usage span invalid: %#v", ref)
		}
		if ref.URL == "https://example.test/b.png" {
			if ref.DefinitionStartByte == nil || ref.DefinitionEndByte == nil {
				t.Fatal("reference definition location missing")
			}
			definition := source[*ref.DefinitionStartByte:*ref.DefinitionEndByte]
			if !strings.Contains(definition, "[flow]") || !strings.Contains(definition, "https://example.test/b.png") {
				t.Fatalf("wrong definition span: %q", definition)
			}
		}
	}
	if !urls["https://example.test/a.png"] || !urls["https://example.test/b.png"] {
		t.Fatalf("wrong resolved URLs: %#v", parsed.ImageRefs)
	}
}

func TestMarkdownHeadingPathsAndBlockBoundaries(t *testing.T) {
	source := "# Root\n\n### Skipped\n\n正文。\n\n## Repeated\n\n- outer\n  - nested\n\n## Repeated\n\n> 引用\n> 第二行\n\nSetext\n======\n\n~~~text\n# code heading\n![not-image](https://example.test/code.png)\n~~~\n\n    # indented code\n\n|A|B|\n|-|-|\n|一|二|\n\n<img src=\"https://example.test/raw.png\">\n"
	parsed, err := ParseMarkdown(source)
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]int{}
	paths := map[string][]string{}
	for _, b := range parsed.Blocks {
		types[b.Type]++
		raw := source[b.StartByte:b.EndByte]
		if strings.Contains(raw, "正文。") {
			paths["skipped"] = b.HeadingPath
		}
		if b.Type == "list" {
			paths["list"] = b.HeadingPath
		}
		if b.Type == "blockquote" {
			paths["quote"] = b.HeadingPath
		}
		if b.Type == "code" {
			for _, h := range b.HeadingPath {
				if strings.Contains(h, "code heading") {
					t.Fatal("code created heading")
				}
			}
		}
	}
	for _, kind := range []string{"heading", "list", "blockquote", "code", "html"} {
		if types[kind] == 0 {
			t.Fatalf("block %s absent: %#v", kind, parsed.Blocks)
		}
	}
	if !reflect.DeepEqual(paths["skipped"], []string{"Root", "Skipped"}) || !reflect.DeepEqual(paths["list"], []string{"Root", "Repeated"}) || !reflect.DeepEqual(paths["quote"], []string{"Root", "Repeated"}) {
		t.Fatalf("wrong heading hierarchy %#v", paths)
	}
	if len(parsed.ImageRefs) != 1 || parsed.ImageRefs[0].Kind != "html" || parsed.ImageRefs[0].Status != "unsupported_html_image" || parsed.ImageRefs[0].URL != "https://example.test/raw.png" {
		t.Fatalf("code image or HTML pending state incorrect: %#v", parsed.ImageRefs)
	}
}

func TestMarkdownUnresolvedImageIsExplicitAndDoesNotResolveFromCode(t *testing.T) {
	source := "# 未解析\n\n![缺少定义][absent]\n\n```text\n[absent]: https://example.test/pretend.png\n```\n"
	parsed, err := ParseMarkdown(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.ImageRefs) != 1 {
		t.Fatalf("missing unresolved image state: %#v", parsed.ImageRefs)
	}
	ref := parsed.ImageRefs[0]
	if ref.Status != "unresolved_reference" || ref.URL != "" || ref.DefinitionStartByte != nil || ref.DefinitionEndByte != nil {
		t.Fatalf("code masqueraded as reference definition: %#v", ref)
	}
	if source[ref.StartByte:ref.EndByte] != "![缺少定义][absent]" {
		t.Fatalf("unresolved image source corrupted: %#v", ref)
	}
}

func TestMarkdownImageSyntaxDoesNotConfuseURLBracketsOrCode(t *testing.T) {
	cases := []struct {
		name, source, url, kind string
		count                   int
	}{
		{"URL brackets", "![a](https://example.test/[b].png)", "https://example.test/[b].png", "inline", 1},
		{"nested formatted alt", "![**嵌套 [标记]**][flow]\n\n[flow]: https://example.test/flow.png\n", "https://example.test/flow.png", "reference", 1},
		{"inline code", "`![code](https://example.test/x.png)`", "", "", 0},
		{"escaped marker", `\![literal](https://example.test/x.png)`, "", "", 0},
		{"malformed", "![unterminated](https://example.test/x.png", "", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := ParseMarkdown(tc.source)
			if err != nil {
				t.Fatal(err)
			}
			if len(parsed.ImageRefs) != tc.count {
				t.Fatalf("refs=%#v", parsed.ImageRefs)
			}
			if tc.count > 0 {
				ref := parsed.ImageRefs[0]
				if tc.kind == "reference" && (ref.DefinitionStartByte == nil || ref.DefinitionEndByte == nil) {
					t.Fatalf("nested reference definition lost: %#v", ref)
				}
				if ref.URL != tc.url || ref.Kind != tc.kind {
					t.Fatalf("wrong image semantics %#v", ref)
				}
				if ref.StartByte < 0 || ref.EndByte > len(tc.source) || ref.EndByte <= ref.StartByte {
					t.Fatalf("invalid image source span %#v", ref)
				}
			}
		})
	}
}

func TestMarkdownTextlessSourcePreservesRawWithoutEmbeddingURL(t *testing.T) {
	for name, source := range map[string]string{"long HTML": "<div>" + strings.Repeat("html-hidden", 1000) + "</div>\r\n", "HTML only": "<img src=\"https://example.test/raw.png\">\r\n", "definition only": "[unused]: https://example.test/unused.png\r\n", "blank only": " \r\n\r\n"} {
		t.Run(name, func(t *testing.T) {
			chunks, err := ChunkMarkdown(source)
			if err != nil {
				t.Fatal(err)
			}
			if len(chunks) == 0 {
				t.Fatal("raw source lost")
			}
			covered := 0
			for _, c := range chunks {
				assertMarkdownRawSpan(t, source, c)
				covered += c.EndByte - c.StartByte
				if strings.TrimSpace(c.EmbeddingContent) != "" {
					t.Fatalf("textless source would leak raw URL to embedding: %#v", c)
				}
			}
			if covered != len(source) {
				t.Fatalf("raw source coverage=%d want%d", covered, len(source))
			}
		})
	}
}

func TestMarkdownMalformedImageDoesNotPanicOrInventUnresolvedRefs(t *testing.T) {
	for _, source := range []string{"![a](x) stray ]", "![a](x) ![b] stray ]", "![a](x) ](y)", "![![nested](x)](y)", "![a](x) [unterminated\n", "![a] [broken\n"} {
		t.Run(source, func(t *testing.T) {
			parsed, err := ParseMarkdown(source)
			if err != nil {
				t.Fatal(err)
			}
			for _, ref := range parsed.ImageRefs {
				if ref.StartByte < 0 || ref.EndByte <= ref.StartByte || ref.EndByte > len(source) {
					t.Fatalf("invalid image span %#v", ref)
				}
				if !strings.HasPrefix(source[ref.StartByte:ref.EndByte], "![") {
					t.Fatalf("invented unresolved ref %#v", ref)
				}
			}
		})
	}
}

func TestMarkdownMultilineReferenceDefinitionAndLongImageHaveExactSourceSpans(t *testing.T) {
	cases := map[string]string{
		"multiline definition": "![流程][flow]\r\n\r\n[flow]:\r\n  <https://example.test/flow.png>\r\n  \"标题\"\r\n",
		"long image":           "# 图片\r\n\r\n![" + strings.Repeat("很长说明", 350) + "](https://example.test/flow.png)\r\n",
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			parsed, err := ParseMarkdown(source)
			if err != nil {
				t.Fatal(err)
			}
			if len(parsed.ImageRefs) != 1 {
				t.Fatalf("image refs=%#v", parsed.ImageRefs)
			}
			ref := parsed.ImageRefs[0]
			if ref.URL != "https://example.test/flow.png" {
				t.Fatalf("resolved URL wrong %#v", ref)
			}
			if source[ref.StartByte:ref.StartByte+2] != "![" || ref.EndByte > len(source) {
				t.Fatalf("usage source wrong %#v", ref)
			}
			if name == "multiline definition" {
				if ref.DefinitionStartByte == nil || ref.DefinitionEndByte == nil {
					t.Fatalf("definition spans absent %#v", ref)
				}
				definition := source[*ref.DefinitionStartByte:*ref.DefinitionEndByte]
				if !strings.Contains(definition, "[flow]") || !strings.Contains(definition, "https://example.test/flow.png") {
					t.Fatalf("multiline URL not covered by definition %q", definition)
				}
			}
			chunks, err := ChunkMarkdown(source)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			covered := make([]bool, len(source))
			for _, chunk := range chunks {
				assertMarkdownRawSpan(t, source, chunk)
				for i := chunk.StartByte; i < chunk.EndByte; i++ {
					covered[i] = true
				}
				for _, childRef := range chunk.ImageRefs {
					if childRef.StartByte == ref.StartByte && childRef.EndByte == ref.EndByte {
						found = true
					}
				}
			}
			if !found {
				t.Fatal("splitting long alt lost original image usage metadata")
			}
			for i, present := range covered {
				if !present {
					t.Fatalf("original byte %d missing", i)
				}
			}
		})
	}
}
