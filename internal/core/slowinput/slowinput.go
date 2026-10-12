// Package slowinput builds the inputs that make a cleaner take minutes instead of milliseconds.
//
// They are upstream tests/slow-inputs.test.mjs's SHAPES and MARKDOWN_SHAPES: half a megabyte of
// cut-open tags, unterminated comments and repeated Markdown openers, each of which used to send a
// regex walker into exponential time. Three packages clean or render them (content, render, archive),
// and they all have to be fast on the same inputs, so the shapes are built here rather than three
// times over.
package slowinput

import "strings"

// Size is how large each shape is: upstream's 500 KB, the biggest a body may be.
const Size = 500_000

// Repeat is a unit repeated to Size.
func Repeat(unit string) string {
	return strings.Repeat(unit, Size/len(unit))
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
func TagSoup(seed int) string {
	state := seed
	next := func() int {
		state = (state*1103515245 + 12345) & 0x7fffffff
		return state
	}
	var b strings.Builder
	b.Grow(Size + 64)
	for b.Len() < Size {
		b.WriteString(pieces[next()%len(pieces)])
	}
	return b.String()
}

// Shapes is upstream's SHAPES: the HTML bodies.
func Shapes() map[string]string {
	return map[string]string{
		"nested <scr<script>ipt>":   Nested(Size / 8),
		"unclosed <script ":         Repeat("<script "),
		"unclosed <style>":          Repeat("<style>"),
		"unclosed comments":         Repeat("<!-- >"),
		"unclosed <link ":           Repeat("<link "),
		"unclosed <a ":              Repeat("<a "),
		"a run of <":                Repeat("<"),
		"unclosed <div ":            Repeat("<div "),
		"unclosed <body ":           Repeat("<body "),
		"unclosed <title>":          Repeat("<title>"),
		"unclosed <head ":           Repeat("<head "),
		"unclosed email-only parts": Repeat("<a data-email-only>"),
		"tag soup":                  TagSoup(1),
		"tag soup with attributes":  TagSoup(2),
		"tag soup with box markers": TagSoup(3),
	}
}

// MarkdownShapes is upstream's MARKDOWN_SHAPES: the same hostility, written as Markdown.
func MarkdownShapes() map[string]string {
	html := Shapes()
	return map[string]string{
		"nested <scr<script>ipt>": html["nested <scr<script>ipt>"],
		"unclosed <script ":       html["unclosed <script "],
		"a run of *a":             Repeat("*a"),
		"a run of *a and spaces":  Repeat("*a "),
		"unclosed boxes":          Repeat(":::box\n"),
		"nested quotes":           Repeat("> "),
		"a run of [a](":           Repeat("[a]("),
		"a run of <":              html["a run of <"],
		"tag soup":                html["tag soup"],
	}
}

// UnsafeTags are the tags that must not survive any of the cleaners. The archive's page and an email
// both end up in a browser, and neither may contain something that can run.
var UnsafeTags = []string{"<script", "<style", "<svg", "<iframe", "<form", "<button",
	"<noscript", "<link", "<object", "<embed", "<template", "<textarea", "<title", "<xmp"}
