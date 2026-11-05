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

On its own in a terminal, `pony` opens a conversation with your last pony, or
asks which one the first time. `pony pepper` opens one with that pony.

```text
  OnePony                  pepper <pepper@onepony.com>  curious
  ──────────────────────────────────────────────────────────────────
  - What's on my calendar tomorrow?

  ┃ Two things: the dentist at 10, and lunch with Dana.

  Pepper is working  12s
  ──────────────────────────────────────────────────────────────────
  > Message Pepper…
```

A pony is already at work somewhere else, so this is joining it rather than
starting something. Leaving doesn't stop it: come back and you're where you
were, with anything it said while you were gone. While you're here your answers
come here; leave, and a later answer is emailed to you. `/new` starts another
chat, `/ponies` talks to another pony, and typing `/` lists them all. If a pony
needs your go-ahead, the conversation says what for, and Ctrl+O or `/open` takes
you to approve it in your browser. Esc, Ctrl+C or Ctrl+D leaves, and asks first
if you have something unsent. For a new line use Shift+Enter, Ctrl+Enter or
Alt+Enter, where the terminal reports them; in Apple's Terminal, Option+Enter
with Use Option as Meta Key turned on. To send a line that starts with a slash,
start it with two. In a pipe or a script, bare `pony` prints the usage.

The commands below work as ever, for one-off asks, scripts and other agents:

```sh
pony login                       # sign in, in your browser
pony whoami                      # the account you're signed in as
pony whoami --json               # and the token: what it reaches (* is every pony), when it ends
pony list                        # your ponies, and what each is doing
pony status pepper               # one pony: what it's doing, and the card it's on
pony log pepper                  # what it's been doing lately
pony log -f                      # what all your ponies are doing, as it happens
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

`pony log` shows what's been happening, oldest first, in the words of the
Feed: `pony log pepper` for one pony, `pony log` for every pony you can reach.
`-n` says how many lines (20 by default, 100 at most). `-f` keeps printing what
happens until Ctrl-C, a few seconds behind; with `--json` it prints NDJSON, one
object per line. It shows what happened, not what was said in a mail: `pony
watch <card>` has a chat's words. Times are ISO 8601, in UTC. `--debug` adds
each line's run and request ids, when it has them.

On a terminal the log is laid out for reading: names in a column, each pony in
its own colour, what was said bright against what happened around it, ✗ for
what went wrong, a card's title once rather than on every line, a space between
cards, and nothing wider than the screen.
`NO_COLOR` takes the colour and leaves the rest. In a pipe, each line is a
whole record, so `pony log | grep quotes` finds them all.

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
