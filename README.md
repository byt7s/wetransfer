<h1 align="center">wetransfer</h1>

<p align="center">
  <em>Send and receive files on <a href="https://wetransfer.com">WeTransfer</a> from the terminal. No browser, no account.</em>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/version-0.2.0-007aff" alt="Version" />
  <img src="https://img.shields.io/badge/macOS%20%C2%B7%20Linux-007aff" alt="macOS and Linux" />
  <a href="go.mod"><img src="https://img.shields.io/github/go-mod/go-version/byt7s/wetransfer?color=007aff" alt="Go version" /></a>
</p>

```bash
wetransfer up ./photos -p random -e 7d
# https://we.tl/t-…
# and the password, printed once
```

- **A folder is one transfer.** It is packed into a single `.zip`. Several paths go in the same link.
- **Large files keep several connections busy.** Files are split into 15 MiB blocks and uploaded in parallel.
- **The network can drop.** Uploads retry and re-sign expired blocks. Downloads keep a `.part` file and resume.
- **Password and expiry stick.** `none`, `random`, or a fixed password, and any duration up to 7 days. Saved for the next upload.
- **Look before you save.** `wetransfer info` prints the title, size, and file list. Download is a separate command.
- **A real screen when you are at the terminal.** Progress, a password prompt, and a choice when the file is already there.
- **Scriptable.** `--json`, signed direct links, stdout output, and several links per command.

## Install

Go 1.25 or newer is required. There are no runtime dependencies.

```bash
git clone https://github.com/byt7s/wetransfer.git
cd wetransfer
go build -o wetransfer ./cmd/wetransfer
install wetransfer /usr/local/bin/wetransfer
wetransfer version
```

Or install into `$GOBIN`:

```bash
go install ./cmd/wetransfer
```

## Commands

| Command | What it does |
|---------|----------------|
| `wetransfer upload PATH…` | Send files or folders and print the link |
| `wetransfer download LINK…` | Save one or more transfers |
| `wetransfer info LINK` | Show the title, size, expiry, and files |
| `wetransfer config` | Show or save the default password and expiry |
| `wetransfer version` | Print the build |

`upload` is also `u` and `up`. `download` is also `down`, `d`, and `dl`. Run `wetransfer <command> -h` for the flags.

## Upload

```bash
wetransfer up report.pdf

wetransfer up -p random -e 7d ./photos

wetransfer upload -p none -e 1d -t Notes a.txt b.txt
```

| Flag | |
|------|--|
| `-p` | `none`, `random`, or a password. Default comes from `config` |
| `-e` | Expiry, such as `12h`, `1d`, `7d`. Maximum is 7 days |
| `-t` | Title |
| `-m` | Message |
| `-c` | Parallel connections (default 4) |
| `-s` | Print only the link and password |
| `--json` | Machine-readable result on stdout |
| `-v` | Show API requests |

Every path goes into one transfer. A directory becomes a single `.zip`, so the link carries one file per folder. Large files are split into 15 MiB blocks; each block has its own MD5 and presigned URL. Ctrl+C cancels and deletes the incomplete transfer on the server.

## Download

```bash
wetransfer down https://we.tl/t-AbCdEfGhIjKl

wetransfer d AbCdEfGhIjKl -p secret -o ~/Downloads

wetransfer down LINK1 LINK2 -o ~/Downloads

wetransfer info AbCdEfGhIjKl
```

| Flag | |
|------|--|
| `-o` | Output directory. Use `-` for stdout with a single-file transfer |
| `-p` | Password, if the transfer has one |
| `-g` | Print signed direct links and download nothing |
| `-y` | Overwrite existing files (also `-f`, `--force`) |
| `-n` | Keep both files |
| `-c` | Parallel connections (default 3) |
| `-s` | No progress and no summary |
| `--json` | Machine-readable result on stdout |
| `-v` | Show API requests |

`LINK` can be a short URL (`https://we.tl/…`), a downloads URL (`https://wetransfer.com/downloads/…`), a handle (`t-AbCd…`), an `id/hash` pair, or a recipient link with `id/recipient/hash`.

At a terminal, a missing password is asked for, and an existing file offers overwrite, keep both, rename, or cancel. Bytes already saved live in `name.part` until the download finishes. Ctrl+C leaves that partial file, and the next download of the same name continues from it. A dropped connection is retried with a fresh Range request.

`info` prints the link, title, password flag, size, expiry, and the files. It does not download anything. A protected transfer still shows the summary; pass `-p` to see the list.

## Scripting

`--json` keeps stdout for data only; progress and errors go to stderr.

```bash
# just the link
wetransfer up report.pdf --json | jq -r .url

# list the files
wetransfer info "$URL" --json | jq -r '.files[] | "\(.name) \(.size)"'

# pipes
wetransfer down "$URL" -g                    # one direct URL per line
wetransfer down "$URL" -o - > file.bin       # single-file transfer
```

**`up --json`**

```json
{"url":"https://we.tl/t-…","password":"…","size":123,"expires_at":"…","files":[{"name":"a.txt","size":123}]}
```

**`info --json`** — `files_hidden=true` when the password is missing

```json
{"url":"…","title":"…","password_protected":false,"size":123,"expires_at":"…","state":"downloadable","number_of_downloads":0,"files":[{"name":"a.txt","size":123}]}
```

**`down --json`** — always an array, one entry per link; failures carry `error`

```json
[{"link":"…","output_dir":".","files":[{"name":"a.txt","path":"./a.txt","size":123}]}]
```

With `-g --json`, each entry has `direct: [{"name","size","url"}]` instead of `files`.

## Defaults

`config` is what an upload uses when you do not pass `-p` or `-e`.

| Password | |
|----------|--|
| `none` | No password |
| `random` | A new password on every upload, printed with the link |
| anything else | That password, every time |

```bash
wetransfer config

wetransfer config -password none -expiry 7d

wetransfer config -password random -expiry 3d

wetransfer config -password 's3cret' -expiry 1d
```

The file is `config.json` under `wetransfer-cli/` in your user config directory: `~/Library/Application Support/wetransfer-cli/config.json` on macOS, `~/.config/wetransfer-cli/config.json` on Linux. Only your account can read it.

## Limits

Anonymous transfers, as enforced by WeTransfer:

| | |
|------|--|
| Maximum size | **3 GiB** per transfer |
| Expiry | **7 days** (default 7d, minimum 1m) |
| Password | Yes, without an account |
| Files | up to 25,000 items |
| Account | Not required |

## Proxy

`HTTP_PROXY`, `HTTPS_PROXY`, and `NO_PROXY` are honored, including the S3 upload and the file download.

```bash
HTTPS_PROXY=http://127.0.0.1:8080 wetransfer up big.bin
```

## How it works

1. `POST /api/v4/transfers` creates the transfer and returns an upload token.
2. The token is a JWT that points at the *storm* endpoints: preflight, block announcement, S3 upload, and batch creation.
3. Each 15 MiB block gets its own MD5 and presigned PUT URL; the batch joins the blocks in order.
4. `PUT /api/v4/transfers/{id}/consolidate` finalizes the transfer and returns the `we.tl` link.

For downloads, `POST /api/v4/transfers/{id}/prepare-download` reads the metadata and `POST /api/v4/transfers/{id}/download` signs a direct link for each file.

No password or credential is baked into the binary. The only local state is `config.json`.

## Development

```bash
go test ./...
go vet ./...
go build -o wetransfer ./cmd/wetransfer
```

```
cmd/wetransfer      entry point
internal/api        API client and storm protocol
internal/app        upload, download, info, link parsing
internal/cli        command and flag parsing
internal/config     config.json and durations (7d, 12h…)
internal/ui         progress, TTY, prompts
internal/zipx       folder packing
```

## Disclaimer

wetransfer is an independent project. It is not affiliated with, endorsed by, or connected to WeTransfer. Product names belong to their owners.

It talks to WeTransfer the same way the website does. The API is not public and can change without notice. You are responsible for what you send and for following [WeTransfer's terms](https://wetransfer.com/legal/terms). The software is provided as is, without warranty.
