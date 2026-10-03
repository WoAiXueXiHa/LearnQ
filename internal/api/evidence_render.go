package api

import (
	"bytes"
	"fmt"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/util"
)

// Source Markdown never loads external images in the browser. Raw HTML and
// dangerous link protocols stay disabled in Goldmark's default HTML renderer.
type evidenceImageRenderer struct{}

func (evidenceImageRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindImage, func(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			_, err := fmt.Fprint(w, "<span class=\"muted\">[图片：请查看固定快照]</span>")
			return ast.WalkSkipChildren, err
		}
		return ast.WalkContinue, nil
	})
}
func renderEvidenceMarkdown(source string) (string, error) {
	md := goldmark.New(goldmark.WithExtensions(extension.Table, extension.Strikethrough), goldmark.WithRendererOptions(renderer.WithNodeRenderers(util.Prioritized(evidenceImageRenderer{}, 100))))
	var out bytes.Buffer
	err := md.Convert([]byte(source), &out)
	return out.String(), err
}
