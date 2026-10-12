// Package slowinput builds the inputs that make a cleaner take minutes instead of milliseconds.
//
// They are upstream tests/slow-inputs.test.mjs's SHAPES and MARKDOWN_SHAPES: half a megabyte of
// cut-open tags, unterminated comments and repeated Markdown openers, each of which used to send a
// regex walker into exponential time. Three packages clean or render them (content, render, archive),
// and they all have to be fast on the same inputs, so the shapes are built here rather than three
// times over.
package slowinput

import "strings"

// The two sizes the linearity tests compare: upstream's spec size (500 KB, the biggest a body may be)
// and a quarter of it. Four times the input must not take eight times the time.
const (
	Small = 125_000
	Spec  = 4 * Small
)

// Repeat is a unit repeated to fill size bytes.
func Repeat(unit string, size int) string {
	return strings.Repeat(unit, size/len(unit))
}

// Nested is upstream's nested: a script tag cut open depth times.
func Nested(depth int) string {
	return strings.Repeat("<scr", depth) + "<script>alert(1)</script>" + strings.Repeat("ipt>", depth)
}

// pieces are upstream's PIECES: the fragments a body can be built out of for as long as it hurts.
var pieces = []string{"<", ">", "</", "<!--", "-->", "<script", "</script>", "<scr", "ipt>",
	"<style>", "<svg", "<link ", "<title>", "<head ", "<body ", "<div ", "</div>", "<a ",
	`<a href="`, "</a>", "<img ", ` src="https://x.test/a.png"`, "<p>", "</p>",
	" data-email-only", " data-preheader", "=", `"`, "'", " ", "\n", "x", "*a", "_", "[", "](",
	"`", "> ", ":::box\n"}

// TagSoup is a deterministic mix of the pieces: adversarial rather than uniform. The generator is a
// stand-in for upstream's seeded random, because what matters is a repeatable shape.
func TagSoup(seed, size int) string {
	state := seed
	next := func() int {
		state = (state*1103515245 + 12345) & 0x7fffffff
		return state
	}
	var b strings.Builder
	b.Grow(size + 64)
	for b.Len() < size {
		b.WriteString(pieces[next()%len(pieces)])
	}
	return b.String()
}

// Shape builds one adversarial input of a given size.
type Shape func(size int) string

// Shapes is upstream's SHAPES: the HTML bodies, at whatever size is asked for.
func Shapes() map[string]Shape {
	repeat := func(unit string) Shape {
		return func(size int) string { return Repeat(unit, size) }
	}
	return map[string]Shape{
		"nested <scr<script>ipt>":   func(size int) string { return Nested(size / 8) },
		"unclosed <script ":         repeat("<script "),
		"unclosed <style>":          repeat("<style>"),
		"unclosed comments":         repeat("<!-- >"),
		"unclosed <link ":           repeat("<link "),
		"unclosed <a ":              repeat("<a "),
		"a run of <":                repeat("<"),
		"unclosed <div ":            repeat("<div "),
		"unclosed <body ":           repeat("<body "),
		"unclosed <title>":          repeat("<title>"),
		"unclosed <head ":           repeat("<head "),
		"unclosed email-only parts": repeat("<a data-email-only>"),
		"tag soup":                  func(size int) string { return TagSoup(1, size) },
		"tag soup with attributes":  func(size int) string { return TagSoup(2, size) },
		"tag soup with box markers": func(size int) string { return TagSoup(3, size) },
	}
}

// MarkdownShapes is upstream's MARKDOWN_SHAPES: the same hostility, written as Markdown.
func MarkdownShapes() map[string]Shape {
	html := Shapes()
	repeat := func(unit string) Shape {
		return func(size int) string { return Repeat(unit, size) }
	}
	return map[string]Shape{
		"nested <scr<script>ipt>": html["nested <scr<script>ipt>"],
		"unclosed <script ":       html["unclosed <script "],
		"a run of *a":             repeat("*a"),
		"a run of *a and spaces":  repeat("*a "),
		"unclosed boxes":          repeat(":::box\n"),
		"nested quotes":           repeat("> "),
		"a run of [a](":           repeat("[a]("),
		"a run of <":              html["a run of <"],
		"tag soup":                html["tag soup"],
	}
}

// UnsafeTags are the tags that must not survive any of the cleaners. The archive's page and an email
// both end up in a browser, and neither may contain something that can run.
var UnsafeTags = []string{"<script", "<style", "<svg", "<iframe", "<form", "<button",
	"<noscript", "<link", "<object", "<embed", "<template", "<textarea", "<title", "<xmp"}
