package tea

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"time"
)

const (
	// defaultFramerate specifies the maximum interval at which we should
	// update the view.
	defaultFramerate = time.Second / 60
)

// RendererIgnoreLinesMsg tells the renderer to skip rendering for the given
// range of lines.
type IgnoreLinesMsg struct {
	from int
	to   int
}

// IgnoreLines is a command that sets a range of lines to be ignored
// by the renderer. The general use case here is that those lines would be
// rendered separately for performance reasons.
func IgnoreLines(from int, to int) IgnoreLinesMsg {
	return IgnoreLinesMsg{from: from, to: to}
}

// ClearIgnoredLinesMsg has the renderer allows the renderer to commence rendering
// any lines previously set to be ignored.
type ClearIgnoredLinesMsg struct{}

// RendererIgnoreLines is a command that sets a range of lines to be ignored
// by the renderer.
func ClearIgnoredLines(from int, to int) ClearIgnoredLinesMsg {
	return ClearIgnoredLinesMsg{}
}

// ScrollDownMsg is experiemental. There are no guarantees about it persisting
// in a future API. It's exposed for high performance scrolling.
type ScrollUpMsg struct {
	newLines       []string
	topBoundary    int
	bottomBoundary int
}

// ScrollDownMsg is experiemental. There are no guarantees about it persisting
// in a future API. It's exposed for high performance scrolling.
type ScrollDownMsg struct {
	newLines       []string
	topBoundary    int
	bottomBoundary int
}

// renderer is a timer-based renderer, updating the view at a given framerate
// to avoid overloading the terminal emulator.
type renderer struct {
	out           io.Writer
	buf           bytes.Buffer
	framerate     time.Duration
	ticker        *time.Ticker
	mtx           sync.Mutex
	done          chan struct{}
	lastRender    string
	linesRendered int

	// renderer size; usually the size of the window
	width  int
	height int

	// lines not to render
	ignoreLines map[int]struct{}
}

// newRenderer creates a new renderer. Normally you'll want to initialize it
// with os.Stdout as the argument.
func newRenderer(out io.Writer) *renderer {
	return &renderer{
		out:       out,
		framerate: defaultFramerate,
	}
}

// start starts the renderer.
func (r *renderer) start() {
	if r.ticker == nil {
		r.ticker = time.NewTicker(r.framerate)
	}
	r.done = make(chan struct{})
	go r.listen()
}

// stop permanently halts the renderer.
func (r *renderer) stop() {
	r.flush()
	r.done <- struct{}{}
}

func (r *renderer) listen() {
	for {
		select {
		case <-r.ticker.C:
			if r.ticker != nil {
				r.flush()
			}
		case <-r.done:
			r.mtx.Lock()
			r.ticker.Stop()
			r.ticker = nil
			r.mtx.Unlock()
			close(r.done)
			return
		}
	}
}

// flush renders the buffer.
func (r *renderer) flush() {
	if r.buf.Len() == 0 || r.buf.String() == r.lastRender {
		// Nothing to do
		return
	}

	// We have an opportunity here to limit the rendering to the terminal width
	// and height, but this would mean a few things:
	//
	// 1) We'd need to maintain the terminal dimensions internally and listen
	// for window size changes. [done]
	//
	// 2) We'd need to measure the width of lines, accounting for multi-cell
	// rune widths, commonly found in Chinese, Japanese, Korean, emojis and so
	// on. We'd use something like go-runewidth
	// (http://github.com/mattn/go-runewidth).
	//
	// 3) We'd need to measure the width of lines excluding ANSI escape
	// sequences and break lines in the right places accordingly.
	//
	// Because of the way this would complicate the renderer, this may not be
	// the place to do that.

	out := new(bytes.Buffer)

	r.mtx.Lock()
	defer r.mtx.Unlock()

	if r.linesRendered > 0 {
		// Clear the lines we painted in the last render.
		for i := r.linesRendered; i > 0; i-- {

			// Check and see if we should skip rendering for this line. That
			// includes clearing the line, which we normally do before a
			// render.
			if _, exists := r.ignoreLines[i]; !exists {
				clearLine(out)
			}

			cursorUp(out)
		}

		if _, exists := r.ignoreLines[0]; !exists {
			clearLine(out)
		}
	}
	r.linesRendered = 0

	for _, b := range r.buf.Bytes() {
		if _, exists := r.ignoreLines[r.linesRendered]; exists {
			cursorDown(out) // skip rendering for this line.
			r.linesRendered++
		} else if b == '\n' {
			out.Write([]byte("\r\n"))
			r.linesRendered++
		} else {
			_, _ = out.Write([]byte{b})
		}
	}

	_, _ = r.out.Write(out.Bytes())
	r.lastRender = r.buf.String()
	r.buf.Reset()
}

// write writes to the internal buffer. The buffer will be outputted via the
// ticker which calls flush().
func (r *renderer) write(s string) {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	r.buf.Reset()
	_, _ = r.buf.WriteString(s)
}

// setIngoredLines speicifies lines not to be touched by the standard Bubble Tea
// renderer.
func (r *renderer) setIgnoredLines(from int, to int) {
	for i := from; i < to; i++ {
		r.ignoreLines[i] = struct{}{}
	}
}

// clearIgnoredLines sets all lines to be rendered by the standard Bubble
// Tea renderer. Any lines previously set to be ignored can be rendered to
// again.
func (r *renderer) clearIgnoredLines() {
	r.ignoreLines = make(map[int]struct{})
}

// insertTop effectively scrolls up. It inserts lines at the top of a given
// area designated to be a scrollable region, pushing everything else down.
// This is roughly how ncurses does it.
//
// For this to work renderer.ignoreLines must be set to ignore the scrollable
// region since we are bypassing the normal Bubble Tea renderer here.
//
// Because this method relies on the terminal dimensions, it's only valid for
// full-window applications (generally those that use the alternate screen
// buffer).
//
// This method bypasses the normal rendering buffer and is philisophically
// different than the normal way we approach rendering in Bubble Tea. It's for
// use in high-performance rendering, such as a pager that could potentially
// be rendering very complicated ansi. In cases where the content is simpler
// standard Bubble Tea rendering should suffice.
func (r *renderer) insertTop(lines []string, topBoundary, bottomBoundary int) {
	r.mtx.Lock()
	defer r.mtx.Unlock()

	b := new(bytes.Buffer)

	saveCursorPosition(b)
	changeScrollingRegion(b, topBoundary, bottomBoundary)
	moveCursor(b, topBoundary, 0)
	insertLine(b, len(lines))
	_, _ = io.WriteString(b, "\r\n"+strings.Join(lines, "\r\n"))
	changeScrollingRegion(b, 0, r.height)
	restoreCursorPosition(b)

	r.out.Write(b.Bytes())
}

// insertBottom effectively scrolls down. It inserts lines at the bottom of
// a given area designated to be a scrollable region, pushing everything else
// up.  This is roughly how ncurses does it.
//
// For this to work renderer.ignoreLines must be set to ignore the scrollable
// region since we are bypassing the normal Bubble Tea renderer here.
//
// See note in insertTop() on how this function only makes sense for
// full-window applications and how it differs from the noraml way we do
// rendering in Bubble Tea.
func (r *renderer) insertBottom(lines []string, topBoundary, bottomBoundary int) {
	r.mtx.Lock()
	defer r.mtx.Unlock()

	b := new(bytes.Buffer)

	saveCursorPosition(b)
	changeScrollingRegion(b, topBoundary, bottomBoundary)
	moveCursor(b, topBoundary, 0)
	_, _ = io.WriteString(b, "\r\n"+strings.Join(lines, "\r\n"))
	changeScrollingRegion(b, 0, r.height)
	restoreCursorPosition(b)

	r.out.Write(b.Bytes())
}