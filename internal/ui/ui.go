// Package ui holds the small terminal helpers: TTY detection, a progress bar,
// and password/overwrite prompts.
package ui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// IsTerminal reports whether f is attached to a terminal.
func IsTerminal(f *os.File) bool {
	return isatty(f.Fd())
}

// Progress is a single aggregate bar written to stderr.
type Progress struct {
	total   int64
	done    atomic.Int64
	label   atomic.Value // string
	out     io.Writer
	enabled bool
	start   time.Time
	stop    chan struct{}
	wg      sync.WaitGroup
}

func NewProgress(total int64, enabled bool, out io.Writer) *Progress {
	p := &Progress{total: total, out: out, enabled: enabled}
	p.label.Store("")
	return p
}

func (p *Progress) Start() {
	if !p.enabled {
		return
	}
	p.start = time.Now()
	p.stop = make(chan struct{})
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		t := time.NewTicker(120 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				p.render(false)
			case <-p.stop:
				p.render(true)
				return
			}
		}
	}()
}

func (p *Progress) Add(n int64) {
	if n > 0 {
		p.done.Add(n)
	}
}

// Rewind removes bytes that were counted for a transfer attempt that failed
// and will be sent again.
func (p *Progress) Rewind(n int64) {
	if n <= 0 {
		return
	}
	for {
		cur := p.done.Load()
		next := cur - n
		if next < 0 {
			next = 0
		}
		if p.done.CompareAndSwap(cur, next) {
			return
		}
	}
}

func (p *Progress) SetLabel(s string) {
	p.label.Store(s)
}

func (p *Progress) Stop() {
	if !p.enabled {
		return
	}
	select {
	case <-p.stop:
	default:
		close(p.stop)
	}
	p.wg.Wait()
}

func (p *Progress) render(final bool) {
	done := p.done.Load()
	label, _ := p.label.Load().(string)

	const width = 24
	var pct float64
	if p.total > 0 {
		pct = float64(done) / float64(p.total)
		if pct > 1 {
			pct = 1
		}
	}
	filled := int(pct * width)
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)

	elapsed := time.Since(p.start).Seconds()
	speed := 0.0
	if elapsed > 0 {
		speed = float64(done) / elapsed
	}

	line := fmt.Sprintf("\r\033[K[%s] %3.0f%%  %s/s", bar, pct*100, FormatBytes(int64(speed)))
	if p.total > 0 && speed > 0 && done < p.total {
		remain := time.Duration(float64(p.total-done)/speed) * time.Second
		line += "  ETA " + FormatClock(remain)
	}
	if label != "" {
		line += "  " + label
	}
	fmt.Fprint(p.out, line)
	if final {
		fmt.Fprintln(p.out)
	}
}

// FormatBytes renders a byte count with binary units.
func FormatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	value := float64(n) / float64(div)
	units := []string{"KB", "MB", "GB", "TB"}
	return fmt.Sprintf("%.1f %s", value, units[exp])
}

// FormatClock renders a duration as mm:ss or hh:mm:ss.
func FormatClock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	h := int(d / time.Hour)
	m := int(d/time.Minute) % 60
	s := int(d/time.Second) % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

// Confirm asks a yes/no question. It returns def when stdin is not a TTY.
func Confirm(prompt string, def bool) bool {
	if !IsTerminal(os.Stdin) {
		return def
	}
	fmt.Fprint(os.Stderr, prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return def
	}
	line = strings.ToLower(strings.TrimSpace(line))
	if line == "" {
		return def
	}
	return line == "y" || line == "yes" || line == "s" || line == "sim"
}

// ReadPassword reads a line without echoing it. Falls back to plain input when
// stty is unavailable.
func ReadPassword(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	cmd := exec.Command("stty", "-echo")
	cmd.Stdin = os.Stdin
	echoOff := cmd.Run() == nil

	var once sync.Once
	restore := func() {
		if !echoOff {
			return
		}
		once.Do(func() {
			c := exec.Command("stty", "echo")
			c.Stdin = os.Stdin
			_ = c.Run()
		})
	}
	defer restore()

	// If the user presses Ctrl+C while typing, put the terminal back into a
	// usable state before exiting. Otherwise the prompt would leave echo off.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	done := make(chan struct{})
	go func() {
		select {
		case <-sig:
			restore()
			fmt.Fprintln(os.Stderr)
			os.Exit(130)
		case <-done:
		}
	}()

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	close(done)
	restore()
	if echoOff {
		fmt.Fprintln(os.Stderr)
	}
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
