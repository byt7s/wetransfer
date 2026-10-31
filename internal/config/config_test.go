package config

import (
	"strings"
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"1d", 24 * time.Hour, true},
		{"7d", 7 * 24 * time.Hour, true},
		{"1.5d", 36 * time.Hour, true},
		{"12h", 12 * time.Hour, true},
		{"30m", 30 * time.Minute, true},
		{"", 0, false},
		{"0d", 0, false},
		{"abc", 0, false},
		{"-1d", 0, false},
	}
	for _, c := range cases {
		got, err := ParseDuration(c.in)
		if c.ok && err != nil {
			t.Errorf("ParseDuration(%q) error: %v", c.in, err)
			continue
		}
		if !c.ok {
			if err == nil {
				t.Errorf("ParseDuration(%q) = %v, want error", c.in, got)
			}
			continue
		}
		if got != c.want {
			t.Errorf("ParseDuration(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	if got := FormatDuration(7 * 24 * time.Hour); got != "7d" {
		t.Errorf("FormatDuration(7d) = %q", got)
	}
	if got := FormatDuration(36 * time.Hour); got != "1.5d" {
		t.Errorf("FormatDuration(36h) = %q", got)
	}
}

func TestPasswordValue(t *testing.T) {
	if p, gen, err := (Config{Password: "none"}).PasswordValue(); p != "" || gen || err != nil {
		t.Errorf("none = %q %v %v", p, gen, err)
	}
	if p, gen, err := (Config{Password: "secret"}).PasswordValue(); p != "secret" || gen || err != nil {
		t.Errorf("fixed = %q %v %v", p, gen, err)
	}
	p, gen, err := (Config{Password: "random"}).PasswordValue()
	if err != nil || !gen || len(p) != 12 {
		t.Errorf("random = %q %v %v", p, gen, err)
	}
	if strings.ContainsAny(p, "0O1lI") {
		t.Errorf("random password contains ambiguous characters: %q", p)
	}
}
