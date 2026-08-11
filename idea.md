# OWG Bot

A [MeshCore](https://meshcore.co.uk) bot by the OneWheelGeek. Mesh nodes DM the bot,
the bot replies with useful things: weather, reminders, gemini browsing, whatever
plugins get written next.

**Repo:** github.com/jclement/owgbot (public)

## Goals

1. **Plugin-first architecture** — adding a new command should mean dropping in one
   small, self-contained module. No touching core code.
2. **Easy deployment** — one command from the dev machine to a running service on the Pi.
3. **Mesh-friendly** — LoRa messages are tiny (~140 bytes usable), slow, and lossy.
   Every reply must be terse by design, and anything longer gets chunked with
   explicit continuation.

## Target environment

- Raspberry Pi Zero 2 W (512 MB RAM — keep the footprint small)
- Heltec V3 running MeshCore companion firmware, attached over USB serial
- Internet access via the Pi's WiFi (needed for weather, gemini, etc.)

**Implementation: Go.** Cross-compile a single static binary for `linux/arm64` —
tiny footprint on the Zero 2 W, trivial deploys, and self-update is just swapping
one file. The official MeshCore client libraries are Python/JS, so the
companion-radio serial protocol gets implemented (or a community Go lib vendored)
as its own `transport` package — which we need as an interface anyway for the fake
radio in dev.

## Interaction model

- Commands use a slash prefix: `/w calgary`, `/remind +5d Buy milk`.
- **Sticky sessions:** the last plugin a client used is remembered per client, and
  bare (non-slash) messages route to it. Example: after `/gem owg.fyi` renders a
  page with numbered links, sending `1` opens link 1, `n` gets the next chunk.
  Slash commands always route globally and switch the session.
- Users are identified by their MeshCore node public key (Ed25519 — cryptographically
  strong, no extra auth needed for per-user state).
- **DM only.** Channel messages are ignored — simpler, and politer on airtime.
- Unknown commands get a one-line pointer to `/help`.
- **Rate limiting per user**, configurable in the config file (requests/minute,
  plus per-plugin caps like max stored reminders). Over-limit gets one terse notice,
  then silence.

## Core commands (v1)

| Command | Behavior |
|---|---|
| `/help` | List commands, one line each. Chunked if needed. |
| `/ver` | Version string: `githash(+dirty) - build date`, embedded at build time via `-ldflags`. |
| `/w [location]` | Short forecast. Flexible input: city name, lat/long, or remembered default per user. Open-Meteo (free, no key, includes geocoding). |
| `/remind +5d Buy milk` | Store a reminder, deliver it as a DM when due. Durations (`+5d`, `+2h30m`) for v1; absolute times later. Persisted so restarts don't lose them. Delivery is best-effort — if the node is unreachable, retry on a backoff for a bounded window. |
| `/gem <url>` | Browse a gemini site. Page rendered as chunked text; links numbered and remembered in the session. `1` follows link 1, `n` next chunk, `b` back. |

## Admin commands

Admins are a list of node public keys in the config file. Non-admins get "unknown
command" for these — don't advertise them in `/help`.

| Command | Behavior |
|---|---|
| `/update` | Check GitHub releases for a newer build; if found, download the `linux/arm64` binary, verify its checksum, swap it in, and restart via systemd. Replies with old → new version. Keep the previous binary alongside for manual rollback. |
| `/plugins` | List loaded plugins and their status. |

## Architecture

- **Core:** connects to the radio, dispatches inbound DMs to plugins, handles
  sessions, chunking, and outbound rate limiting (LoRa airtime is a shared resource
  — throttle and queue outbound messages).
- **Plugin API:** each plugin declares its commands + help text, gets a context with:
  - `reply(text)` — auto-chunked
  - per-`(plugin, user)` key-value store
  - session hooks (receive bare messages while sticky)
  - optional scheduler hook (for reminders and future periodic plugins)
- **State:** single SQLite file (KV store namespaced by plugin+user, sessions,
  reminder queue). Lives in `/var/lib/owgbot/`.
- **Config:** `/etc/owgbot/config.yml` — serial port, admin node keys, rate limits,
  plugin enable/disable, plugin settings. Live-reloaded on file change.

## Dev tooling (mise)

- `mise run dev` — runs the bot against a **fake radio** with a CLI test UI: type a
  message, see the reply, fixed test client ID. No hardware needed; the transport is
  an interface so the fake and the real serial radio are interchangeable.
- `mise run test` — unit tests (plugins should be testable against the fake transport).
- `mise run deploy` — cross-compiles for `linux/arm64` and deploys to the Pi over SSH:
  - host/user come from `mise.local.toml` (gitignored)
  - copy the binary, install/refresh the systemd unit, create `/etc/owgbot/config.yml`
    from template if absent (never overwrite), create state dirs, restart service

Two update paths, same artifact:
1. **`mise run deploy`** — push from the dev machine (bootstrap + day-to-day dev).
2. **`/update` (admin DM)** — the bot pulls the latest GitHub release build and
   swaps itself. GitHub Actions builds and publishes the `linux/arm64` release
   binary + checksums on tag.
