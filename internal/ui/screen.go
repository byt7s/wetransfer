package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/term"
)

const (
	ansiReset  = "\033[0m"
	ansiBold   = "\033[1m"
	ansiDim    = "\033[2m"
	ansiCyan   = "\033[36m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
)

type StepState int

const (
	StepPending StepState = iota
	StepCurrent
	StepDone
)

type Step struct {
	Label string
	State StepState
}

// Screen is a full-screen alternate buffer for uploads and downloads. It is a
// no-op unless enabled (stderr is a terminal and the run is not silent).
type Screen struct {
	out     io.Writer
	enabled bool

	mu      sync.Mutex
	section string
	phase   string
	label   string
	steps   []Step
	done    int64
	total   int64
	speed   float64
	eta     time.Duration
	start   time.Time
	entered bool
}

func NewScreen(out io.Writer, enabled bool) *Screen {
	return &Screen{out: out, enabled: enabled}
}

func (s *Screen) Enabled() bool { return s.enabled }

// Enter switches to the alternate screen and hides the cursor.
func (s *Screen) Enter() {
	if !s.enabled {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entered {
		return
	}
	fmt.Fprint(s.out, "\033[?1049h\033[?25l\033[2J\033[H")
	s.entered = true
	s.start = time.Now()
	s.drawLocked()
}

// Exit restores the normal screen. It is safe to call twice.
func (s *Screen) Exit() {
	if !s.enabled {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.entered {
		return
	}
	fmt.Fprint(s.out, "\033[0m\033[?25h\033[?1049l")
	s.entered = false
}

func (s *Screen) SetSection(section string) {
	s.mu.Lock()
	s.section = section
	s.drawLocked()
	s.mu.Unlock()
}

func (s *Screen) SetPhase(phase string) {
	s.mu.Lock()
	s.phase = phase
	s.drawLocked()
	s.mu.Unlock()
}

func (s *Screen) SetSteps(steps []Step) {
	s.mu.Lock()
	s.steps = append([]Step(nil), steps...)
	s.drawLocked()
	s.mu.Unlock()
}

// ClearProgress resets the bar when one phase ends and another begins.
func (s *Screen) ClearProgress() {
	s.mu.Lock()
	s.label = ""
	s.done = 0
	s.total = 0
	s.speed = 0
	s.eta = 0
	s.drawLocked()
	s.mu.Unlock()
}

func (s *Screen) SetProgress(label string, done, total int64, speed float64, eta time.Duration) {
	s.mu.Lock()
	s.label = label
	s.done = done
	s.total = total
	s.speed = speed
	s.eta = eta
	s.drawLocked()
	s.mu.Unlock()
}

func (s *Screen) drawLocked() {
	if !s.enabled || !s.entered {
		return
	}
	width := contentWidth(os.Stderr)

	var b strings.Builder
	b.WriteString("\033[H")
	writeLine(&b, "")
	writeLine(&b, "  "+tone(ansiBold+ansiCyan, "wetransfer"))
	writeLine(&b, "  "+tone(ansiDim, strings.Repeat("─", width)))
	writeLine(&b, "")

	for _, step := range s.steps {
		switch step.State {
		case StepDone:
			writeLine(&b, "  "+tone(ansiGreen, "✓")+"  "+tone(ansiDim, clipRunes(step.Label, width-4)))
		case StepCurrent:
			writeLine(&b, "  "+tone(ansiBold+ansiCyan, "▸")+"  "+tone(ansiBold, clipRunes(step.Label, width-4)))
		default:
			writeLine(&b, "  "+tone(ansiDim, "·")+"  "+tone(ansiDim, clipRunes(step.Label, width-4)))
		}
	}
	if len(s.steps) > 0 {
		writeLine(&b, "")
	}

	if s.section != "" {
		writeLine(&b, "  "+tone(ansiBold, clipRunes(s.section, width)))
	}
	detail := s.phase
	if s.label != "" && !strings.EqualFold(s.label, s.phase) {
		if detail != "" {
			detail += "  ·  "
		}
		detail += s.label
	}
	if detail != "" {
		writeLine(&b, "  "+tone(ansiDim, clipRunes(detail, width)))
	}

	if s.total > 0 {
		pct := float64(s.done) / float64(s.total) * 100
		if pct > 100 {
			pct = 100
		}
		filled := int((float64(width) * pct / 100) + 0.5)
		if filled <= 0 && pct > 0 {
			filled = 1
		}
		if filled > width {
			filled = width
		}
		bar := tone(ansiCyan, strings.Repeat("█", filled)) + tone(ansiDim, strings.Repeat("░", width-filled))
		writeLine(&b, "")
		writeLine(&b, "  "+bar)
		writeLine(&b, "")
		writeLine(&b, "  "+tone(ansiBold, fmt.Sprintf("%.0f%%", pct))+"   "+
			FormatBytes(s.done)+" of "+FormatBytes(s.total))
		speed := "--"
		if s.speed > 0 {
			speed = FormatBytes(int64(s.speed)) + "/s"
		}
		eta := "--"
		if s.eta > 0 {
			eta = FormatClock(s.eta) + " left"
		}
		stats := speed + "   ·   " + eta + "   ·   " + FormatClock(time.Since(s.start))
		writeLine(&b, "  "+tone(ansiDim, clipRunes(stats, width)))
	} else if s.done > 0 {
		writeLine(&b, "")
		writeLine(&b, "  "+FormatBytes(s.done))
	}

	writeLine(&b, "")
	writeLine(&b, "  "+tone(ansiBold, "ctrl+c")+" "+tone(ansiDim, "cancel"))
	b.WriteString("\033[J")
	fmt.Fprint(s.out, b.String())
}

func writeLine(b *strings.Builder, line string) {
	b.WriteString(line)
	b.WriteString("\033[K\r\n")
}

func contentWidth(f *os.File) int {
	width, _, err := term.GetSize(int(f.Fd()))
	if err != nil || width < 24 {
		width = 80
	}
	width -= 4
	if width > 68 {
		width = 68
	}
	if width < 20 {
		width = 20
	}
	return width
}

func tone(code, text string) string {
	if text == "" {
		return ""
	}
	if os.Getenv("NO_COLOR") != "" {
		return text
	}
	return code + text + ansiReset
}

func clipRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	if n == 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}
