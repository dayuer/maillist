// Package content renders and cleans the HTML of campaigns: the :::box callout extension,
// Markdown rendering, allow-list cleaning and thumbnails.
package content

import (
	"regexp"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// CalloutAttributes is the marker boxes carry on the archive page.
const CalloutAttributes = ` data-callout=""`

// EmailBoxTag replaces the whole opening tag of a box in email: mail clients only apply inline
// styles, and the bottom padding is shorter because the last paragraph keeps its margin.
const EmailBoxTag = `<div style="margin:24px 0;padding:24px 24px 8px;border-radius:12px;background:#f4f4f5;">`

// Upstream CALLOUT_TAG_PATTERN.
var calloutTagPattern = regexp.MustCompile(`<div\b[^>]*\bdata-callout\b[^>]*>`)

// InlineCalloutStyles swaps every <div … data-callout …> for EmailBoxTag.
func InlineCalloutStyles(html string) string {
	return calloutTagPattern.ReplaceAllLiteralString(html, EmailBoxTag)
}

// A box starts with a ":::box" line and ends with a ":::" line. Both patterns match a whole line,
// including its newline, because goldmark hands PeekLine the line with the terminator.
var (
	calloutOpenPattern  = regexp.MustCompile(`^:::box[ \t]*\r?\n?$`)
	calloutClosePattern = regexp.MustCompile(`^:::[ \t]*\r?\n?$`)
)

// NodeKind of a callout box: a container whose lines were parsed as ordinary blocks.
var kindCallout = ast.NewNodeKind("Callout")

type calloutNode struct{ ast.BaseBlock }

func (n *calloutNode) Kind() ast.NodeKind { return kindCallout }

func (n *calloutNode) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, nil, nil)
}

// scanFailedFromKey keys the per-parse record of a lookahead that already ran to the end of the
// source. Keeping it in parser.Context, rather than in the parser, keeps MarkdownToHTML safe to
// call concurrently.
var scanFailedFromKey = parser.NewContextKey()

// closerAhead reports whether a closing ":::" line follows before the end of the source. Without a
// closer the box is not a box: the ":::box" line stays literal text, exactly as upstream's
// tokenizer falls back to plain Markdown. A lookahead that reaches the end of the source is
// remembered, so a document full of unclosed boxes stays a linear number of line reads.
func closerAhead(reader text.Reader, pc parser.Context) bool {
	startLine, startSeg := reader.Position()
	start := startSeg.Start
	if failedFrom, ok := pc.Get(scanFailedFromKey).(int); ok && start >= failedFrom {
		return false
	}
	reader.AdvanceLine()
	for {
		line, segment := reader.PeekLine()
		if segment.IsEmpty() {
			pc.Set(scanFailedFromKey, start)
			reader.SetPosition(startLine, startSeg)
			return false
		}
		if calloutClosePattern.Match(line) {
			reader.SetPosition(startLine, startSeg)
			return true
		}
		reader.AdvanceLine()
	}
}

// advancePastLine consumes the marker's line but not its newline: the block loop advances past that
// itself. A closing marker on the last line of the document has no newline to leave, and consuming
// one byte less there left the final ":" behind — it rendered as its own paragraph (the phase 1-3
// audit's FI-A1).
func advancePastLine(reader text.Reader, line []byte) {
	advance := len(line)
	if advance > 0 && line[advance-1] == '\n' {
		advance--
	}
	reader.Advance(advance)
}

type calloutParser struct{}

func (p *calloutParser) Trigger() []byte { return []byte(":") }

func (p *calloutParser) Open(parent ast.Node, reader text.Reader, pc parser.Context) (ast.Node, parser.State) {
	line, _ := reader.PeekLine()
	if !calloutOpenPattern.Match(line) || !closerAhead(reader, pc) {
		return nil, parser.NoChildren
	}
	advancePastLine(reader, line)
	return &calloutNode{}, parser.NoChildren
}

func (p *calloutParser) Continue(node ast.Node, reader text.Reader, pc parser.Context) parser.State {
	line, segment := reader.PeekLine()
	if segment.IsEmpty() {
		return parser.Close
	}
	if calloutClosePattern.Match(line) {
		advancePastLine(reader, line)
		return parser.Close
	}
	return parser.Continue | parser.HasChildren
}

func (p *calloutParser) Close(node ast.Node, reader text.Reader, pc parser.Context) {}

// CanInterruptParagraph lets a box end the paragraph above it without a blank line, as upstream does.
func (p *calloutParser) CanInterruptParagraph() bool { return true }

func (p *calloutParser) CanAcceptIndentedLine() bool { return false }

type calloutRenderer struct{ isEmail bool }

func (r calloutRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(kindCallout, r.render)
}

func (r calloutRenderer) render(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		if r.isEmail {
			_, _ = w.WriteString(EmailBoxTag)
		} else {
			_, _ = w.WriteString("<div" + CalloutAttributes + ">")
		}
		return ast.WalkContinue, nil
	}
	_, _ = w.WriteString("</div>\n")
	return ast.WalkContinue, nil
}

// calloutExtension adds the box syntax to a goldmark parser and renderer.
type calloutExtension struct{ isEmail bool }

func (e calloutExtension) Extend(m goldmark.Markdown) {
	m.Parser().AddOptions(parser.WithBlockParsers(
		util.Prioritized(&calloutParser{}, 950),
	))
	m.Renderer().AddOptions(renderer.WithNodeRenderers(
		util.Prioritized(calloutRenderer(e), 500),
	))
}
