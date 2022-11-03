package md

import (
	"fmt"
	"regexp"
	"strings"
)

// FmtCodec is a codec that formats Markdown in a specific style.
//
// The only supported configuration option is the text width.
//
// The formatted text uses the following style:
//
//   - Blocks are always separated by a blank line.
//
//   - Thematic breaks always use "***".
//
//   - Code blocks always use the fenced syntax; in other words, indented code
//     blocks are never used.
//
//   - Code fences use backquotes (like "```"), except when the info string
//     contains a backquote, in which case tildes are used (like "~~~").
//
//   - Container blocks never omit their markers; in other words, lazy
//     continuation is never used.
//
//   - Bullet lists use "-" as a marker, except when following immediately after
//     another bullet list that already uses "-", in which case "*" is used.
//
//   - Ordered lists use "X." (X being a number) as a marker, except when
//     following immediately after another ordered list that already uses "X.",
//     in which case "X)" is used.
//
//   - Emphasis always uses "*".
//
//   - Strong emphasis always uses "**".
//
//   - Hard line break always uses an explicit "\".
type FmtCodec struct {
	pieces []string
	Width  string

	unsupported *FmtUnsupported

	// Current active container blocks.
	containers stack[*fmtContainer]
	// The value of len(pieces) when the last container block was started. Used
	// to determine whether a container is empty, in which case a blank line is
	// needed to preserve the container.
	containerStart int

	// The punctuation of the just popped list container, only populated if the
	// last Op was OpBulletListEnd or OpOrderedListEnd. Used to alternate list
	// punctuation when a list follows directly after another of the same type.
	poppedListPunct rune
	// Whether the next new stanza should be suppressed.
	suppressNewStanza bool
	// Whether a new stanza was already started.
	lastOpType OpType
}

// FmtUnsupported contains information about use of unsupported features.
type FmtUnsupported struct {
	// Input contains emphasis or strong emphasis nested in another emphasis or
	// strong emphasis (not necessarily of the same type).
	NestedEmphasisOrStrongEmphasis bool
	// Input contains emphasis or strong emphasis that follows immediately after
	// another emphasis or strong emphasis (not necessarily of the same type).
	ConsecutiveEmphasisOrStrongEmphasis bool
}

func (c *FmtCodec) String() string { return strings.Join(c.pieces, "") }

// Unsupported returns information about use of unsupported features that may
// make the output incorrect. It returns nil if there is no use of unsupported
// features.
func (c *FmtCodec) Unsupported() *FmtUnsupported { return c.unsupported }

func (c *FmtCodec) setUnsupported() *FmtUnsupported {
	if c.unsupported == nil {
		c.unsupported = &FmtUnsupported{}
	}
	return c.unsupported
}

var (
	escapeText = strings.NewReplacer(
		"[", `\[`, "]", `\]`, "*", `\*`, "`", "\\`", `\`, `\\`,
		"&", "&amp;", "<", "&lt;",
		// TODO: Don't escape _ when in-word
		"_", "\\_",
	).Replace
)

var (
	backquoteRunRegexp = regexp.MustCompile("`+")
	tildeRunRegexp     = regexp.MustCompile("~+")
)

func (c *FmtCodec) Do(op Op) {
	var poppedListPunct rune
	defer func() {
		c.poppedListPunct = poppedListPunct
	}()

	switch op.Type {
	case OpThematicBreak, OpHeading, OpCodeBlock, OpHTMLBlock, OpParagraph,
		OpBlockquoteStart, OpBulletListStart, OpOrderedListStart:
		if c.suppressNewStanza {
			c.suppressNewStanza = false
		} else {
			if len(c.pieces) > 0 && c.lastOpType != OpBlockquoteStart && c.lastOpType != OpListItemStart {
				c.writeLine("")
			}
		}
	}
	if op.MissingCloser {
		c.suppressNewStanza = true
	}
	c.lastOpType = op.Type

	switch op.Type {
	case OpThematicBreak:
		c.writeLine("***")
	case OpHeading:
		c.startLine()
		c.write(strings.Repeat("#", op.Number) + " ")
		c.doInlineContent(op.Content, true)
		c.finishLine()
	case OpCodeBlock:
		startFence, endFence := codeFences(op.Info, op.Lines)
		c.writeLine(startFence)
		for _, line := range op.Lines {
			c.writeLine(line)
		}
		if !op.MissingCloser {
			c.writeLine(endFence)
		}
	case OpHTMLBlock:
		for _, line := range op.Lines {
			c.writeLine(line)
		}
	case OpParagraph:
		c.startLine()
		c.doInlineContent(op.Content, false)
		c.finishLine()
	case OpBlockquoteStart:
		c.containerStart = len(c.pieces)
		c.containers.push(&fmtContainer{typ: fmtBlockquote, marker: "> "})
	case OpBlockquoteEnd:
		if c.containerStart == len(c.pieces) {
			c.writeLine("")
		}
		c.containers.pop()
	case OpListItemStart:
		c.containerStart = len(c.pieces)
		// Set marker to start marker
		if ct := c.containers.peek(); ct.typ == fmtBulletItem {
			ct.marker = fmt.Sprintf("%c   ", ct.punct)
		} else {
			ct.marker = fmt.Sprintf("%d%c ", ct.number, ct.punct)
			if len(ct.marker) < 4 {
				ct.marker += strings.Repeat(" ", 4-len(ct.marker))
			}
		}
	case OpListItemEnd:
		if c.containerStart == len(c.pieces) {
			// When a list item is empty, we will write a line consisting of
			// bullet punctuations and spaces only. When there are at least 3
			// instances of the same punctuation, this line will be become a
			// thematic break instead. Avoid this by varying the punctuation.
			for i, ct := range c.containers {
				if i >= 2 && identicalBulletMarkers(c.containers[i-2:i+1]) {
					ct.punct = pickPunct('-', '*', ct.punct)
					ct.marker = fmt.Sprintf("%c   ", ct.punct)
				}
			}
			c.writeLine("")
		}
		c.containers.peek().number++
	case OpBulletListStart:
		c.containers.push(&fmtContainer{
			typ:   fmtBulletItem,
			punct: pickPunct('-', '*', c.poppedListPunct)})
	case OpBulletListEnd:
		poppedListPunct = c.containers.pop().punct
	case OpOrderedListStart:
		c.containers.push(&fmtContainer{
			typ:    fmtOrderedItem,
			punct:  pickPunct('.', ')', c.poppedListPunct),
			number: op.Number})
	case OpOrderedListEnd:
		poppedListPunct = c.containers.pop().punct
	}
}

func codeFences(info string, lines []string) (string, string) {
	var fenceRune rune
	var runLens map[int]bool
	if strings.ContainsRune(info, '`') {
		fenceRune = '~'
		runLens = matchLens(lines, tildeRunRegexp)
	} else {
		fenceRune = '`'
		runLens = matchLens(lines, backquoteRunRegexp)
	}
	l := 3
	for x := range runLens {
		if l < x+1 {
			l = x + 1
		}
	}
	fence := strings.Repeat(string(fenceRune), l)
	if fenceRune == '~' && strings.HasPrefix(info, "~") {
		return fence + " " + escapeText(info), fence
	}
	return fence + escapeText(info), fence
}

func identicalBulletMarkers(containers []*fmtContainer) bool {
	for _, ct := range containers {
		if ct.typ != fmtBulletItem || ct.marker != containers[0].marker {
			return false
		}
	}
	return true
}

var (
	leadingHashesRegexp  = regexp.MustCompile(`^#{1,6}`)
	trailingHashesRegexp = regexp.MustCompile(`#+$`)
	leadingNumberRegexp  = regexp.MustCompile(`^([0-9]{1,9})([.)])`)
)

func (c *FmtCodec) doInlineContent(ops []InlineOp, atxHeading bool) {
	emphasis := 0
	prevIsEmphasisEnd := false

	for i, op := range ops {
		switch op.Type {
		case OpText:
			text := op.Text
			startOfLine := i == 0 || (ops[i-1].Type == OpNewLine && (i-1 == 0 || ops[i-2].Type != OpNewLine))
			endOfLine := i == len(ops)-1 || ops[i+1].Type == OpNewLine
			if startOfLine && endOfLine && thematicBreakRegexp.MatchString(text) {
				c.write(`\`)
				c.write(text)
				continue
			}
			if startOfLine || i == 0 {
				switch text[0] {
				case ' ':
					c.write("&#32;")
					text = text[1:]
				case '\t':
					c.write("&Tab;")
					text = text[1:]
				case '-', '+':
					if !atxHeading {
						tail := text[1:]
						if startsWithSpaceOrTab(tail) || (tail == "" && endOfLine) {
							c.write(`\` + text[:1])
							text = tail
						}
					}
				case '>':
					if !atxHeading {
						c.write(`\>`)
						text = text[1:]
					}
				case '#':
					if !atxHeading {
						if hashes := leadingHashesRegexp.FindString(text); hashes != "" {
							tail := text[len(hashes):]
							if startsWithSpaceOrTab(tail) || (tail == "" && endOfLine) {
								c.write(`\` + hashes)
								text = tail
							}
						}
					}
				default:
					if !atxHeading {
						if m := leadingNumberRegexp.FindStringSubmatch(text); m != nil {
							tail := text[len(m[0]):]
							if startsWithSpaceOrTab(tail) || (tail == "" && endOfLine) {
								number, punct := m[1], m[2]
								if i == 0 || strings.TrimLeft(number, "0") == "1" {
									c.write(number)
									c.write(`\` + punct)
									text = tail
								}
							}
						} else if strings.HasPrefix(text, "~~~") {
							c.write(`\~~~`)
							text = text[3:]
						}
					}
				}
			}
			suffix := ""
			if endOfLine && text != "" {
				switch text[len(text)-1] {
				case ' ':
					suffix = "&#32;"
					text = text[:len(text)-1]
				case '\t':
					suffix = "&Tab;"
					text = text[:len(text)-1]
				case '#':
					if atxHeading {
						if hashes := trailingHashesRegexp.FindString(text); hashes != "" {
							head := text[:len(text)-len(hashes)]
							if endsWithSpaceOrTab(head) || (head == "" && i == 0) {
								text = head
								suffix = `\` + hashes
							}
						}
					}
				}
			} else if strings.HasSuffix(text, "!") && i < len(ops)-1 && ops[i+1].Type == OpLinkStart {
				text = text[:len(text)-1]
				suffix = `\!`
			}
			c.write(escapeText(text))
			c.write(suffix)
		case OpRawHTML:
			c.write(op.Text)
		case OpNewLine:
			if i == 0 || ops[i-1].Type == OpNewLine {
				c.write("&NewLine;")
			} else {
				c.finishLine()
				c.startLine()
			}
		case OpCodeSpan:
			text := op.Text
			hasRunWithLen := matchLens([]string{text}, backquoteRunRegexp)
			l := 1
			for hasRunWithLen[l] {
				l++
			}
			delim := strings.Repeat("`", l)
			// Code span text is never empty
			first := text[0]
			last := text[len(text)-1]
			addSpace := first == '`' || last == '`' || (first == ' ' && last == ' ' && strings.Trim(text, " ") != "")
			c.write(delim)
			if addSpace {
				c.write(" ")
			}
			c.write(text)
			if addSpace {
				c.write(" ")
			}
			c.write(delim)
		case OpEmphasisStart:
			c.write("*")
			emphasis++
			if emphasis >= 2 {
				c.setUnsupported().NestedEmphasisOrStrongEmphasis = true
			}
			if prevIsEmphasisEnd {
				c.setUnsupported().ConsecutiveEmphasisOrStrongEmphasis = true
			}
		case OpEmphasisEnd:
			c.write("*")
			emphasis--
		case OpStrongEmphasisStart:
			c.write("**")
			emphasis++
			if emphasis >= 2 {
				c.setUnsupported().NestedEmphasisOrStrongEmphasis = true
			}
			if prevIsEmphasisEnd {
				c.setUnsupported().ConsecutiveEmphasisOrStrongEmphasis = true
			}
		case OpStrongEmphasisEnd:
			c.write("**")
			emphasis--
		case OpLinkStart:
			c.write("[")
		case OpLinkEnd:
			c.write("]")
			c.writeLinkTail(op.Dest, op.Text)
		case OpImage:
			c.write("![" + escapeText(op.Alt) + "]")
			c.writeLinkTail(op.Dest, op.Text)
		case OpHardLineBreak:
			c.write("\\")
		}
		prevIsEmphasisEnd = op.Type == OpEmphasisEnd || op.Type == OpStrongEmphasisEnd
	}
}

func startsWithSpaceOrTab(s string) bool {
	return s != "" && (s[0] == ' ' || s[0] == '\t')
}

func endsWithSpaceOrTab(s string) bool {
	return s != "" && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t')
}

func matchLens(pieces []string, pattern *regexp.Regexp) map[int]bool {
	hasRunWithLen := make(map[int]bool)
	for _, piece := range pieces {
		for _, run := range pattern.FindAllString(piece, -1) {
			hasRunWithLen[len(run)] = true
		}
	}
	return hasRunWithLen
}

func (c *FmtCodec) writeLinkTail(dest, title string) {
	c.write("(" + escapeLinkDest(dest))
	if title != "" {
		c.write(" " + wrapAndEscapeLinkTitle(title))
	}
	c.write(")")
}

const asciiControlOrSpaceOrParens = "\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c\x0d\x0e\x0f\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f ()"

func escapeLinkDest(dest string) string {
	if strings.ContainsAny(dest, asciiControlOrSpaceOrParens) {
		return "<" + strings.ReplaceAll(escapeText(dest), ">", "&gt;") + ">"
	}
	return escapeText(dest)
}

var escapeParens = strings.NewReplacer("(", `\(`, ")", `\)`).Replace

func wrapAndEscapeLinkTitle(title string) string {
	doubleQuotes := strings.Count(title, "\"")
	if doubleQuotes == 0 {
		return "\"" + escapeText(title) + "\""
	}
	singleQuotes := strings.Count(title, "'")
	if singleQuotes == 0 {
		return "'" + escapeText(title) + "'"
	}
	parens := strings.Count(title, "(") + strings.Count(title, ")")
	if parens == 0 {
		return "(" + escapeText(title) + ")"
	}
	switch {
	case doubleQuotes <= singleQuotes && doubleQuotes <= parens:
		return `"` + strings.ReplaceAll(escapeText(title), `"`, `\"`) + `"`
	case singleQuotes <= parens:
		return "'" + strings.ReplaceAll(escapeText(title), "'", `\'`) + "'"
	default:
		return "(" + escapeParens(escapeText(title)) + ")"
	}
}

func (c *FmtCodec) startLine() {
	for _, container := range c.containers {
		// TODO: Remove trailing spaces on empty lines
		c.write(container.useMarker())
	}
}

func (c *FmtCodec) finishLine() {
	c.write("\n")
}

func (c *FmtCodec) writeLine(s string) {
	c.startLine()
	c.write(s)
	c.finishLine()
}

func (c *FmtCodec) write(s string) {
	c.pieces = append(c.pieces, s)
}

type fmtContainer struct {
	typ    fmtContainerType
	punct  rune   // punctuation used to build the marker
	number int    // only used when typ == fmtOrderedItem
	marker string // starter or continuation marker
}

type fmtContainerType uint

const (
	fmtBlockquote fmtContainerType = iota
	fmtBulletItem
	fmtOrderedItem
)

func (ct *fmtContainer) useMarker() string {
	m := ct.marker
	if ct.typ != fmtBlockquote {
		ct.marker = strings.Repeat(" ", len(m))
	}
	return m
}

func pickPunct(def, alt, banned rune) rune {
	if def != banned {
		return def
	}
	return alt
}
