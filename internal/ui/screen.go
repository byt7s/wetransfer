package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

// Screen is a full-screen alternate buffer for uploads and downloads.
// It is a no-op unless enabled (stderr is a terminal and the run is not
// silent).
type Screen struct {
	out     io.Writer
	enabled bool

	mu      sync.Mutex
	section string
	phase   string
	label   string
	done    int64
	total   int64
	speed   float64
	eta     time.Duration
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
	width := 80
	if w, _, err := term.GetSize(int(os.Stderr.Fd())); err == nil && w > 0 {
		width = w
	}
	if width < 40 {
		width = 40
	}
	if width > 100 {
		width = 100
	}

	var b strings.Builder
	b.WriteString("\033[H")
	writeLine(&b, "\033[1;34mwetransfer\033[0m")
	writeLine(&b, "\033[2m"+strings.Repeat("─", width-2)+"\033[0m")
	writeLine(&b, "")
	if s.section != "" {
		writeLine(&b, "\033[1m"+s.section+"\033[0m")
	}
	if s.phase != "" {
		writeLine(&b, s.phase)
	}

	if s.total > 0 {
		pct := float64(s.done) / float64(s.total)
		if pct > 1 {
			pct = 1
		}
		barWidth := width - 32
		if barWidth < 12 {
			barWidth = 12
		}
		if barWidth > 48 {
			barWidth = 48
		}
		filled := int(pct * float64(barWidth))
		bar := "\033[34m" + strings.Repeat("█", filled) + "\033[0m" +
			"\033[2m" + strings.Repeat("░", barWidth-filled) + "\033[0m"
		line := fmt.Sprintf("[%s] %3.0f%%", bar, pct*100)
		if s.speed > 0 {
			line += fmt.Sprintf("  %s/s", FormatBytes(int64(s.speed)))
		}
		if s.eta > 0 && s.done < s.total {
			line += "  ETA " + FormatClock(s.eta)
		}
		writeLine(&b, line)
		stats := fmt.Sprintf("%s of %s", FormatBytes(s.done), FormatBytes(s.total))
		if s.label != "" {
			stats += "  ·  " + s.label
		}
		writeLine(&b, stats)
	} else if s.label != "" {
		writeLine(&b, s.label)
	}

	b.WriteString("\r\n")
	writeLine(&b, "\033[2mctrl+c cancel\033[0m")
	b.WriteString("\033[J")
	fmt.Fprint(s.out, b.String())
}

func writeLine(b *strings.Builder, line string) {
	b.WriteString(line)
	b.WriteString("\033[K\r\n")
}
