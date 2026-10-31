// Package cli implements the command line interface.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"wetransfer/internal/app"
	"wetransfer/internal/config"
	"wetransfer/internal/ui"
)

const Version = "0.2.0"

const usage = `wetransfer — send and receive files on WeTransfer from the terminal.

Usage:
  wetransfer up [flags] PATH...      Send files or folders and print the link
  wetransfer down [flags] LINK...    Save one or more transfers
  wetransfer info [flags] LINK       Show the title, size and file list
  wetransfer config [flags]          Show or save the default password and expiry
  wetransfer version                 Print the build

Aliases: up = u = upload, down = d = dl = download

Upload flags:
  -p, --password VALUE   none, random, or a password      (default: config)
  -e, --expiry VALUE     12h, 1d, 7d — maximum 7 days     (default: config)
  -t, --title TEXT       Title shown on the download page
  -m, --message TEXT     Message shown with the link
  -c, --connections N    Parallel connections             (default 4)
  -s, --silent           No progress on stderr
      --json             Machine-readable result on stdout
  -v, --verbose          Show HTTP requests

Download flags:
  -o, --output DIR|-     Output directory, or - for stdout
  -p, --password VALUE   Password, when the transfer has one
  -g, --direct           Print signed direct links, download nothing
  -y, --overwrite        Overwrite existing files (also -f, --force)
  -n, --keep-both        Keep both files
  -c, --connections N    Parallel connections             (default 3)
  -s, --silent           No progress and no summary
      --json             Machine-readable result on stdout
  -v, --verbose          Show HTTP requests

Info flags:
  -p, --password VALUE   Password, when the transfer has one
      --json             Machine-readable result on stdout
  -v, --verbose          Show HTTP requests

Config flags:
  -p, --password VALUE   none, random, or a fixed password
  -e, --expiry VALUE     12h, 1d, 7d — maximum 7 days

Folders are packed into one .zip. No account is needed. Anonymous
transfers are limited to 3 GiB and can live up to 7 days. Large files
are split into 15 MiB blocks and uploaded over several connections.

Examples:
  wetransfer up ./photos -p random -e 7d
  wetransfer up -t Notes -m "for the team" report.pdf
  wetransfer up big.iso --json | jq -r .url
  wetransfer down https://we.tl/t-AbCdEfGhIjKl -o ~/Downloads
  wetransfer down we.tl/t-AbCdEfGhIjKl -g
  wetransfer down we.tl/t-AbCdEfGhIjKl -o - > file.bin
  wetransfer info we.tl/t-AbCdEfGhIjKl -p secret --json
  wetransfer config -password random -expiry 7d

HTTP_PROXY, HTTPS_PROXY and NO_PROXY are honored.
`

// Run executes one command and returns a process exit code.
func Run(ctx context.Context, argv []string) int {
	if len(argv) == 0 {
		fmt.Fprint(os.Stdout, usage)
		return 0
	}

	cmd := argv[0]
	rest := argv[1:]
	switch cmd {
	case "up", "u", "upload":
		return runUpload(ctx, rest)
	case "down", "d", "dl", "download":
		return runDownload(ctx, rest)
	case "info":
		return runInfo(ctx, rest)
	case "config":
		return runConfig(rest)
	case "version", "-v", "--version":
		fmt.Printf("wetransfer %s\n", Version)
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(os.Stdout, usage)
		return 0
	default:
		return fail(fmt.Errorf("unknown command %q\n\nRun 'wetransfer help' for usage", cmd))
	}
}

func fail(err error) int {
	if errors.Is(err, context.Canceled) || errors.Is(err, app.ErrCancelled) {
		fmt.Fprintln(os.Stderr, "Cancelled.")
		return 130
	}
	fmt.Fprintf(os.Stderr, "wetransfer: %v\n", err)
	return 1
}

type uploadJSON struct {
	URL       string             `json:"url"`
	Password  string             `json:"password,omitempty"`
	Size      int64              `json:"size"`
	ExpiresAt string             `json:"expires_at,omitempty"`
	Files     []app.UploadedFile `json:"files,omitempty"`
}

func runUpload(ctx context.Context, argv []string) int {
	cfg, _, err := config.Load()
	if err != nil {
		return fail(err)
	}

	var (
		passwordFlag = cfg.Password
		expiryFlag   = cfg.Expiry
		title        string
		message      string
		silent       bool
		verbose      bool
		jsonOut      bool
		conns        = 4
		paths        []string
	)

	sc := &scanner{args: argv}
	for {
		arg, ok := sc.next()
		if !ok {
			break
		}
		if arg == "--" {
			paths = append(paths, sc.rest()...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			paths = append(paths, arg)
			continue
		}
		key, inline, hasInline := parseFlag(arg)
		switch key {
		case "help":
			fmt.Fprint(os.Stdout, usage)
			return 0
		case "password":
			v, err := sc.value(arg, inline, hasInline)
			if err != nil {
				return fail(err)
			}
			passwordFlag = v
		case "expiry":
			v, err := sc.value(arg, inline, hasInline)
			if err != nil {
				return fail(err)
			}
			expiryFlag = v
		case "title":
			v, err := sc.value(arg, inline, hasInline)
			if err != nil {
				return fail(err)
			}
			title = v
		case "message":
			v, err := sc.value(arg, inline, hasInline)
			if err != nil {
				return fail(err)
			}
			message = v
		case "connections":
			v, err := sc.value(arg, inline, hasInline)
			if err != nil {
				return fail(err)
			}
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 16 {
				return fail(errors.New("connections must be between 1 and 16"))
			}
			conns = n
		case "silent":
			silent = true
		case "verbose":
			verbose = true
		case "json":
			jsonOut = true
		default:
			return fail(fmt.Errorf("unknown flag %q", arg))
		}
	}
	if len(paths) == 0 {
		return fail(errors.New("nothing to send\n\nUsage: wetransfer up [flags] PATH..."))
	}

	expiry, err := config.ParseDuration(expiryFlag)
	if err != nil {
		return fail(err)
	}
	if expiry > config.MaxExpiry {
		return fail(fmt.Errorf("anonymous transfers can live at most %s", config.FormatDuration(config.MaxExpiry)))
	}
	if expiry < time.Minute {
		return fail(errors.New("expiry must be at least 1m"))
	}

	password, _, err := config.Config{Password: passwordFlag}.PasswordValue()
	if err != nil {
		return fail(err)
	}

	res, err := app.Upload(ctx, app.UploadOptions{
		Paths:       paths,
		Password:    password,
		Expiry:      expiry,
		Title:       title,
		Message:     message,
		Silent:      silent,
		Verbose:     verbose,
		Concurrency: conns,
		Err:         os.Stderr,
	})
	if err != nil {
		return fail(err)
	}
	tr := res.Transfer

	if jsonOut {
		err := json.NewEncoder(os.Stdout).Encode(uploadJSON{
			URL:       tr.ShortenedURL,
			Password:  password,
			Size:      tr.Size,
			ExpiresAt: tr.ExpiresAt,
			Files:     res.Files,
		})
		if err != nil {
			return fail(err)
		}
		return 0
	}

	if silent {
		fmt.Fprintln(os.Stdout, tr.ShortenedURL)
		if password != "" {
			fmt.Fprintln(os.Stdout, password)
		}
		return 0
	}

	fmt.Fprintf(os.Stdout, "\nLink:      %s\n", tr.ShortenedURL)
	if password != "" {
		fmt.Fprintf(os.Stdout, "Password:  %s\n", password)
	}
	fmt.Fprintf(os.Stdout, "Size:      %s\n", ui.FormatBytes(tr.Size))
	if tr.ExpiresAt != "" {
		fmt.Fprintf(os.Stdout, "Expires:   %s\n", app.FormatExpiry(tr.ExpiresAt))
	}
	if len(res.Files) > 0 {
		fmt.Fprintf(os.Stdout, "Files:     %d\n", len(res.Files))
	}
	return 0
}

type downloadJSON struct {
	Link      string               `json:"link"`
	OutputDir string               `json:"output_dir,omitempty"`
	Files     []app.DownloadedFile `json:"files,omitempty"`
	Direct    []app.DirectFile     `json:"direct,omitempty"`
	Error     string               `json:"error,omitempty"`
}

func runDownload(ctx context.Context, argv []string) int {
	var (
		password  string
		output    = "."
		overwrite bool
		keepBoth  bool
		silent    bool
		verbose   bool
		jsonOut   bool
		direct    bool
		conns     = 3
		links     []string
	)

	sc := &scanner{args: argv}
	for {
		arg, ok := sc.next()
		if !ok {
			break
		}
		if arg == "--" {
			links = append(links, sc.rest()...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			links = append(links, arg)
			continue
		}
		key, inline, hasInline := parseFlag(arg)
		switch key {
		case "help":
			fmt.Fprint(os.Stdout, usage)
			return 0
		case "password":
			v, err := sc.value(arg, inline, hasInline)
			if err != nil {
				return fail(err)
			}
			password = v
		case "output":
			v, err := sc.value(arg, inline, hasInline)
			if err != nil {
				return fail(err)
			}
			output = v
		case "connections":
			v, err := sc.value(arg, inline, hasInline)
			if err != nil {
				return fail(err)
			}
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 16 {
				return fail(errors.New("connections must be between 1 and 16"))
			}
			conns = n
		case "overwrite":
			overwrite = true
		case "keep-both":
			keepBoth = true
		case "silent":
			silent = true
		case "verbose":
			verbose = true
		case "json":
			jsonOut = true
		case "direct":
			direct = true
		default:
			return fail(fmt.Errorf("unknown flag %q", arg))
		}
	}
	if len(links) == 0 {
		return fail(errors.New("missing link\n\nUsage: wetransfer down [flags] LINK..."))
	}
	if overwrite && keepBoth {
		return fail(errors.New("-y/--overwrite and -n/--keep-both cannot be used together"))
	}
	if output == "-" && len(links) > 1 {
		return fail(errors.New("stdout output accepts a single link"))
	}
	if output == "-" && jsonOut {
		return fail(errors.New("--json and -o - cannot be used together"))
	}

	var entries []downloadJSON
	exitCode := 0
	for _, link := range links {
		entry := downloadJSON{Link: link}
		if !direct {
			entry.OutputDir = output
		}

		if direct {
			files, err := app.DirectLinks(ctx, link, password, verbose, os.Stderr)
			if err != nil {
				if errors.Is(err, app.ErrCancelled) || errors.Is(err, context.Canceled) {
					return fail(err)
				}
				if jsonOut {
					entry.Error = err.Error()
					entries = append(entries, entry)
					exitCode = 1
					continue
				}
				fmt.Fprintf(os.Stderr, "wetransfer: %v\n", err)
				exitCode = 1
				continue
			}
			if jsonOut {
				entry.Direct = files
				entries = append(entries, entry)
				continue
			}
			for _, f := range files {
				fmt.Fprintln(os.Stdout, f.URL)
			}
			continue
		}

		files, err := app.Download(ctx, app.DownloadOptions{
			Link:        link,
			Password:    password,
			OutputDir:   output,
			Overwrite:   overwrite,
			KeepBoth:    keepBoth,
			Silent:      silent,
			Verbose:     verbose,
			Concurrency: conns,
			Stdout:      os.Stdout,
			Err:         os.Stderr,
		})
		if err != nil {
			if errors.Is(err, app.ErrCancelled) || errors.Is(err, context.Canceled) {
				return fail(err)
			}
			if jsonOut {
				entry.Error = err.Error()
				entries = append(entries, entry)
				exitCode = 1
				continue
			}
			fmt.Fprintf(os.Stderr, "wetransfer: %v\n", err)
			exitCode = 1
			continue
		}
		if jsonOut {
			entry.Files = files
			entries = append(entries, entry)
			continue
		}
		if !silent && output != "-" {
			if len(links) > 1 {
				fmt.Fprintf(os.Stdout, "%s: ", link)
			}
			fmt.Fprintf(os.Stdout, "Saved %d file%s to %s\n", len(files), pluralFiles(len(files)), output)
		}
	}

	if jsonOut {
		if err := json.NewEncoder(os.Stdout).Encode(entries); err != nil {
			return fail(err)
		}
	}
	return exitCode
}

func runInfo(ctx context.Context, argv []string) int {
	var (
		password string
		verbose  bool
		jsonOut  bool
		link     string
	)
	sc := &scanner{args: argv}
	for {
		arg, ok := sc.next()
		if !ok {
			break
		}
		if arg == "--" {
			if rest := sc.rest(); len(rest) > 0 {
				link = rest[0]
			}
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			link = arg
			continue
		}
		key, inline, hasInline := parseFlag(arg)
		switch key {
		case "help":
			fmt.Fprint(os.Stdout, usage)
			return 0
		case "password":
			v, err := sc.value(arg, inline, hasInline)
			if err != nil {
				return fail(err)
			}
			password = v
		case "verbose":
			verbose = true
		case "json":
			jsonOut = true
		default:
			return fail(fmt.Errorf("unknown flag %q", arg))
		}
	}
	if link == "" {
		return fail(errors.New("missing link\n\nUsage: wetransfer info [flags] LINK"))
	}

	info, err := app.GetInfo(ctx, link, password, verbose, os.Stderr)
	if err != nil {
		return fail(err)
	}
	if jsonOut {
		if err := json.NewEncoder(os.Stdout).Encode(info); err != nil {
			return fail(err)
		}
		return 0
	}
	printInfo(os.Stdout, info)
	return 0
}

func printInfo(out io.Writer, info *app.TransferInfo) {
	fmt.Fprintf(out, "Link:      %s\n", info.URL)
	if info.Title != "" {
		fmt.Fprintf(out, "Title:     %s\n", info.Title)
	}
	if info.Message != "" && info.Message != "null" {
		fmt.Fprintf(out, "Message:   %s\n", info.Message)
	}
	if info.PasswordProtected {
		fmt.Fprintf(out, "Password:  yes\n")
	} else {
		fmt.Fprintf(out, "Password:  no\n")
	}
	if info.Size == 0 && info.FilesHidden {
		fmt.Fprintf(out, "Size:      hidden (pass -p)\n")
	} else {
		fmt.Fprintf(out, "Size:      %s\n", ui.FormatBytes(info.Size))
	}
	if info.ExpiresAt != "" {
		fmt.Fprintf(out, "Expires:   %s\n", app.FormatExpiry(info.ExpiresAt))
	}
	fmt.Fprintf(out, "State:     %s\n", info.State)
	if info.Downloads > 0 {
		fmt.Fprintf(out, "Downloads: %d\n", info.Downloads)
	}
	if info.FilesHidden {
		fmt.Fprintln(out, "Files:     hidden (pass -p to list)")
		return
	}
	if len(info.Files) == 0 {
		fmt.Fprintln(out, "Files:     none")
		return
	}
	fmt.Fprintf(out, "Files:     %d\n", len(info.Files))
	for _, f := range info.Files {
		fmt.Fprintf(out, "  %-40s %s\n", f.Name, ui.FormatBytes(f.Size))
	}
}

func runConfig(argv []string) int {
	cfg, path, err := config.Load()
	if err != nil {
		return fail(err)
	}

	var (
		password = cfg.Password
		expiry   = cfg.Expiry
		changed  bool
	)
	sc := &scanner{args: argv}
	for {
		arg, ok := sc.next()
		if !ok {
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			return fail(fmt.Errorf("unknown argument %q", arg))
		}
		key, inline, hasInline := parseFlag(arg)
		switch key {
		case "help":
			fmt.Fprint(os.Stdout, usage)
			return 0
		case "password":
			v, err := sc.value(arg, inline, hasInline)
			if err != nil {
				return fail(err)
			}
			password = v
			changed = true
		case "expiry":
			v, err := sc.value(arg, inline, hasInline)
			if err != nil {
				return fail(err)
			}
			expiry = v
			changed = true
		default:
			return fail(fmt.Errorf("unknown argument %q", arg))
		}
	}

	if !changed {
		fmt.Fprintf(os.Stdout, "Password: %s\n", cfg.Password)
		fmt.Fprintf(os.Stdout, "Expiry:   %s\n", cfg.Expiry)
		fmt.Fprintf(os.Stdout, "File:     %s\n", path)
		return 0
	}

	if password != "none" && password != "random" && strings.TrimSpace(password) == "" {
		return fail(errors.New("password cannot be empty; use none"))
	}
	d, err := config.ParseDuration(expiry)
	if err != nil {
		return fail(err)
	}
	if d > config.MaxExpiry {
		return fail(fmt.Errorf("anonymous transfers can live at most %s", config.FormatDuration(config.MaxExpiry)))
	}
	if d < time.Minute {
		return fail(errors.New("expiry must be at least 1m"))
	}
	saved, err := config.Save(config.Config{Password: password, Expiry: expiry})
	if err != nil {
		return fail(err)
	}
	fmt.Fprintf(os.Stdout, "Saved %s\n", saved)
	fmt.Fprintf(os.Stdout, "Password: %s\n", password)
	fmt.Fprintf(os.Stdout, "Expiry:   %s\n", expiry)
	return 0
}

// scanner walks arguments and supports flags before or after positional
// values, plus -p=value and --password=value forms.
type scanner struct {
	args []string
	i    int
}

func (s *scanner) next() (string, bool) {
	if s.i >= len(s.args) {
		return "", false
	}
	v := s.args[s.i]
	s.i++
	return v, true
}

func (s *scanner) rest() []string {
	out := s.args[s.i:]
	s.i = len(s.args)
	return out
}

func (s *scanner) value(flag, inline string, hasInline bool) (string, error) {
	if hasInline {
		return inline, nil
	}
	v, ok := s.next()
	if !ok {
		return "", fmt.Errorf("%s needs a value", flag)
	}
	return v, nil
}

// parseFlag normalizes -p, --password and -password into one key.
func parseFlag(arg string) (key, value string, hasValue bool) {
	s := strings.TrimLeft(arg, "-")
	if i := strings.IndexByte(s, '='); i >= 0 {
		value = s[i+1:]
		hasValue = true
		s = s[:i]
	}
	switch s {
	case "p", "password":
		key = "password"
	case "e", "expiry":
		key = "expiry"
	case "t", "title":
		key = "title"
	case "m", "message":
		key = "message"
	case "c", "connections":
		key = "connections"
	case "o", "output":
		key = "output"
	case "s", "silent":
		key = "silent"
	case "v", "verbose":
		key = "verbose"
	case "y", "f", "overwrite", "force":
		key = "overwrite"
	case "n", "keep-both", "keep_both":
		key = "keep-both"
	case "g", "direct":
		key = "direct"
	case "json":
		key = "json"
	case "h", "help":
		key = "help"
	}
	return key, value, hasValue
}

func pluralFiles(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
