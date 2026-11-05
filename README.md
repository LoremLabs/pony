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

## Sign in

```sh
pony login
```

Your browser opens on OnePony. Sign in there if you aren't already, and
allow pony. It gets a token for all your ponies that lasts 90 days, saved in
`~/.config/pony/credentials.json`, which only you can read. The token is
listed on your Account page, where you can revoke it; `pony logout` revokes
it and forgets it here.

Over ssh, or if the browser doesn't open, go to the address `pony login`
prints, in any browser, and paste the code that page shows. `--no-browser`
goes straight to that.

Scripts and CI use a token instead: make one on your Account page, under
Command line (https://www.onepony.com/settings/account). Name it, choose
which ponies it reaches and how long it lasts. It's shown once.

```sh
export ONEPONY_TOKEN=pony_…
pony whoami
```

`ONEPONY_TOKEN`, when it is set, comes before the token `pony login` saved. A
token is as good as your account for the ponies it reaches, so keep it as
you would a password.

## Use

```sh
pony login                       # sign in, in your browser
pony whoami                      # the account you're signed in as
pony whoami --json               # and the token: what it reaches (* is every pony), when it ends
pony list                        # your ponies, and what each is doing
pony status pepper               # one pony: what it's doing, and the card it's on
pony ask pepper "What's on my calendar tomorrow?"
pony ask pepper - < notes.txt    # the words from stdin
pony watch wch_3f9a…             # follow a card until the pony is done with it
pony version
pony help [command]
```

`pony ask` sends a line to a pony and waits for its answer, printing what the
pony says as it comes. Each ask starts a chat of its own, a card on the
pony's board; `--card` carries on one. A line is 4000 characters at most. If
the pony needs you first (a mail to approve, a question), `ask` says so,
links the card on your board and exits 5. `--timeout` (10 minutes by
default) or Ctrl-C stops the waiting, not the pony, and `pony watch <card>`
picks it up from there. A line sent again after a network failure reaches
the pony once.

While they wait in a terminal, `ask` and `watch` show a pony ambling about and
grazing, with how long it's been (a plain spinner on Windows, without 256
colours, or under `NO_COLOR`). It goes to stderr and clears itself: a pipe, a
file and `--json` never see it.

While `ask` or `watch` is following a card, the answer comes back here and
isn't emailed too, unless you've asked for every answer by email. If nothing
is following when the pony finishes, the answer is emailed to you.

`pony watch` prints what was said on a card, then each new line, until the
pony is done with it: exit 0 when it is, 5 when it waits on you, 7 when its
run failed. `--json` prints NDJSON, one object per line said and per change
of state.

Flags go anywhere on the line (`--` ends them). `--server URL` (or
`ONEPONY_SERVER`) talks to another OnePony server; plain `http://` is allowed
only for `localhost`. Output for people goes to stdout, anything else to
stderr, and `--json` prints JSON. A token is never printed.

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
