# owgbot

A [MeshCore](https://meshcore.co.uk) bot by the OneWheelGeek. DM the bot from
any mesh node and it answers: weather, reminders, gemini browsing, and
whatever plugin gets written next.

Runs on a Raspberry Pi Zero 2 W with a Heltec V3 (MeshCore companion
firmware) on USB serial. Single static Go binary; the companion serial
protocol is implemented in-repo ([internal/transport/meshcore](internal/transport/meshcore/)).

## Commands

| Command | What it does |
|---|---|
| `/help` | BBS-style menu: one chunk of categories (`1=games 2=mesh 3=tools`), reply with a number to drill in; `/help all` for everything |
| `/ver` | bot version |
| `/w [location]` | terse forecast — city or `lat,lon`, remembers your last |
| `/remind +5d Buy milk` | durable reminder DM'd when due (`/remind` lists, `/remind del N` cancels) |
| `/gem <url>` | browse gemini — links numbered, then `1` follows, `n` next page, `b` back |
| `/ping` | pong + the SNR you were heard at — keeps reporting until you send `off` |
| `/seen <node>` / `/nodes` | mesh radar: when nodes were last heard (adverts + messages) |
| `/mail <node> <text>` | store-and-forward mail, delivered when the recipient is next heard |
| `/wall [text]` | the graffiti wall |
| `/wordle` | daily word, shared mesh-wide; guesses are just bare 5-letter messages |
| `/ai <question>` | LLM over LoRa (only present when an OpenAI key is configured) |
| `/zork` | AI-invented micro text adventure, different every game — saves survive restarts (needs the OpenAI key) |
| `/tl` | random BBS-style tagline (mesh puns, ~200 of them) |

Bare (non-slash) messages go to the last session-style plugin you used —
that's how `1`/`n`/`b` reach the gemini browser.

Admin commands (node keys listed in config; invisible to everyone else):
`/plugins`, `/update` (self-update from GitHub releases).

## CLI

One binary, normal subcommands:

```
owgbot serve     run the bot against a real radio
owgbot tui       dev mode: fake radio + chat TUI (plain REPL when piped)
owgbot doctor    check config, data dir, serial port, radio, network
owgbot install   bootstrap this host (root; installs systemd service)
owgbot version   print version
```

Configuration is layered — flags beat environment beats config file beats
built-in defaults:

| Flag | Env | Meaning |
|---|---|---|
| `-c` / `-config` | `OWGBOT_CONFIG` | config file (default `/etc/owgbot/config.yml`; missing = built-in defaults) |
| `-p` / `-port` | `OWGBOT_PORT` | serial port (default: config value, else auto-detect) |
| `-d` / `-data` | `OWGBOT_DATA_DIR` | data dir (default: config value, `dev-data/` without a config) |

Flag/env overrides survive config live-reloads.

## Dev

Requires [mise](https://mise.jdx.dev).

```sh
mise run dev     # owgbot tui — fake radio + chat TUI, no hardware needed
mise run dev:hw  # owgbot serve — real Heltec on USB serial, local state
mise run doctor  # owgbot doctor — is everything plugged in and reachable?
mise run test    # vet + tests
```

The dev TUI is a chat with the bot (you're `deadbeef0001`, an admin). Each
bot bubble is one radio chunk, tagged with its byte cost. `ctrl+l` toggles a
log pane (logs never interleave with the chat), `esc`/`ctrl+c` quits. With
piped stdin it falls back to a plain line REPL, so `printf '/ver\n' | go run . tui`
works for scripted smoke tests.

`dev:hw` runs the real MeshCore transport with the board plugged into your
dev machine — state goes to `dev-data/`, the serial port is auto-detected
(`-p /dev/cu.usbserial-0001` to pin it). Want admins or other config for
local hardware runs? Put them in `hw.local.yml` (gitignored) and point at it
with `OWGBOT_CONFIG=hw.local.yml` in your `mise.local.toml` — then DM the
bot from another node and watch the logs.

## Deploy

The binary installs itself — the systemd unit and config template are
embedded, so bootstrapping any Pi is:

```sh
sudo ./owgbot install
```

That creates the `owgbot` user, `/opt/owgbot` + `/var/lib/owgbot` +
`/etc/owgbot/config.yml` (first run only — never overwritten), installs the
unit, and enables + starts the service. Idempotent; re-run it to upgrade.

From the dev machine it's one command:

```sh
cp mise.local.toml.example mise.local.toml   # set your Pi's host/user
mise run deploy                              # cross-compile, scp, remote install
```

Config is live-reloaded on save. To make yourself an admin, DM the bot once,
grab your node key prefix from `journalctl -u owgbot`, and add it to
`admins:` in the config.

After the first deploy, updates can also ship themselves: tag a release
(`git tag v0.1.0 && git push --tags`), GitHub Actions builds it, and an admin
DMs the bot `/update`.

## Writing a plugin

One package under [internal/plugins/](internal/plugins/) implementing
[plugin.Plugin](internal/plugin/plugin.go) (plus `SessionHandler` if it wants
follow-up messages), registered in [main.go](main.go). Each plugin gets a
private namespaced KV store, a logger, and a send hook — see
[internal/plugins/weather/weather.go](internal/plugins/weather/weather.go)
for the shape.

Keep replies terse: every ~130 bytes is another LoRa transmission.
