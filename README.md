# pony

Talk to your [OnePony](https://www.onepony.com) ponies from the terminal.

`pony` is a small command line client for the OnePony API. People, shell
scripts and other agents can use it to reach the ponies they already have.

## Install

macOS, with Homebrew:

```sh
brew install loremlabs/onepony/pony
```

macOS and Linux, with the install script:

```sh
curl -fsSL https://get.onepony.com/install.sh | bash
```

The script downloads the latest release, checks it against the release's
`checksums.txt`, and installs `pony` to `~/.local/bin`, or to
`/usr/local/bin` when run as root. Set `VERSION=v0.1.0` for a given release
or `PREFIX=/some/dir` for another location. Read it first if you like:
[install.sh](install.sh).

Windows, and anyone who prefers a manual install: download the archive for
your system from the [releases page](https://github.com/LoremLabs/pony/releases)
and put `pony` (or `pony.exe`) on your PATH.

The binaries are not code signed. Installs with Homebrew or the script run
without a prompt. An archive downloaded with a browser on macOS is held by
Gatekeeper until you allow it under System Settings › Privacy & Security, or
run `xattr -d com.apple.quarantine pony`. Windows may show a SmartScreen
notice the first time.

## Use

```sh
pony version
pony help
```

Signing in and talking to a pony are on the way: `pony login`, `pony list`,
`pony ask`, `pony status` and `pony watch`.

### Exit codes

Scripts can rely on these:

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | any other error |
| 2 | bad command, flag or argument |
| 3 | not signed in, or not allowed |
| 4 | no such pony or card |
| 5 | the pony is waiting on you |
| 6 | timed out, or the network failed |
| 7 | the pony's work failed |

## Build from source

```sh
go build -o pony ./cmd/pony
```

## Releasing

Push a tag. `v0.1.0` publishes a release and updates the Homebrew formula in
[LoremLabs/homebrew-onepony](https://github.com/LoremLabs/homebrew-onepony);
`v0.1.0-rc1` publishes a prerelease and leaves the formula alone. The
workflow needs one secret, `HOMEBREW_TAP_TOKEN`: a fine-grained token with
read and write access to the contents of the tap repository only.

## License

MIT
