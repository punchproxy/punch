# Punch

<p align="center">
  <strong>A transparent proxy daemon you drive from the command line.</strong>
</p>

<p align="center">
  <img alt="Use at your own risk" src="https://img.shields.io/badge/vibeshit-100%25-ebc034?style=for-the-badge">
  <a href="https://github.com/punchproxy/punch"><img alt="Go version" src="https://img.shields.io/badge/go-1.25+-00ADD8?style=for-the-badge&logo=go&logoColor=white"></a>
  <a href="https://github.com/spf13/cobra"><img alt="CLI" src="https://img.shields.io/badge/cli-cobra-6E40C9?style=for-the-badge"></a>
  <a href="https://github.com/metacubex/mihomo"><img alt="Mihomo powered" src="https://img.shields.io/badge/poweredby-mihomo-FF6B35?style=for-the-badge"></a>
</p>

Punch routes your machine's traffic through proxies based on rules you control — without touching individual apps. You point your system DNS at it, it captures matching traffic on a TUN device, and forwards it through whichever relay you've selected. Everything is configured live through a single CLI; there are no YAML files to hand-edit and restart.

If you've used Clash or Mihomo, Punch is in the same family — but built around a long-running daemon (`punchd`) and an operator CLI (`punchctl`) instead of a config-file-and-reload workflow.

## What you can do with it

- **Route by domain or CIDR.** Send `*.work.example` through one relay, `*.home.example` through another, and everything else direct.
- **Use DoH for DNS.** Out of the box, Punch resolves through `doh.pub` and `dns.alidns.com` over HTTPS.
- **Subscribe to a proxy provider.** Drop in a Mihomo-style subscription URL and Punch will fetch, refresh, and health-check the relays.
- **Pick the fastest relay automatically.** Punch periodically probes each relay against `generate_204` and switches to the lowest-latency one.
- **See what's going where.** `punchctl dns trace` streams live DNS decisions; `punchctl sessions` shows active flows with bytes, destination, and which relay handled them.
- **Terminate flows.** Kill a stuck session by ID, or all of them.

## Install

### Homebrew (macOS / Linux)

```sh
brew tap punchproxy/punch
brew install punch
```

### Docker

```sh
docker run -d --name punch \
  --network host \
  --cap-add NET_ADMIN \
  --device /dev/net/tun \
  -v punch-data:/var/lib/punch \
  ghcr.io/punchproxy/punch:latest
```

Then drive it from inside the container:

```sh
docker exec punch punchctl status
```

Notes for Docker:
- `--network host` is recommended — transparent capture via TUN is only useful when sharing the host network namespace.
- Without host networking, expose ports explicitly: `-p 127.0.0.1:28854:28854 -p 28853:28853/udp`.
- The daemon's database lives at `/var/lib/punch` inside the container; mount a volume to persist it.
- If `/dev/net/tun` is missing on the host, run `modprobe tun` first.

### From source

```sh
go build -o punchd ./cmd/punchd
go build -o punchctl ./cmd/punchctl
```

## First run

Start the daemon (it needs root to open the TUN device):

```sh
sudo punchd
```

In another terminal, check it's alive:

```sh
punchctl status
```

At this point Punch is running with default rules but no relays — traffic still goes direct. Add a relay group to actually proxy something:

```sh
# From a subscription URL
punchctl relaygroups create main \
  --url https://your-provider.example/sub.yaml \
  --select auto

# Or from a local file
punchctl relaygroups create main --provider-file ./relays.yaml --select auto
```

Confirm relays loaded and pick the fastest one:

```sh
punchctl relays
punchctl relaygroups check main
```

Watch routing decisions stream by:

```sh
punchctl dns trace
```

That's the loop: `punchctl` to inspect, change, and watch; `punchd` keeps running.

## Common tasks

### Route specific domains or CIDRs

```sh
# Add a CIDR to always route through TUN (e.g. a service whose DNS you don't control)
punchctl system routes create 1.1.1.0/24

# Or import a CIDR list from a URL
punchctl system routes create https://core.telegram.org/resources/cidr.txt

# Inspect / remove
punchctl system routes
punchctl system routes delete 1.1.1.0/24
```

DNS rule management:

```sh
punchctl dns rules         # list current rules
punchctl dns upstreams     # see DoH upstreams
punchctl dns routes        # see CIDR routes
punchctl dns cache         # peek at the cache
punchctl dns cache flush
punchctl dns fakeips       # see allocated fake IPs
```

### Switch relays

```sh
punchctl relays                    # list everything across all groups
punchctl relays select hk-1        # pin a specific relay
punchctl relaygroups refresh main  # re-fetch from subscription URL
punchctl relaygroups check main    # re-run health checks
```

### Inspect or kill traffic

```sh
punchctl sessions
punchctl sessions get <SESSION_ID>
punchctl sessions terminate <SESSION_ID>
punchctl sessions terminate --all
```

### Tune output

All list commands support filtering, sorting, and structured output:

```sh
punchctl relays -o wide
punchctl relays -o json
punchctl relays --field-selector status=alive --sort-by .latency_ms
punchctl sessions -o custom-columns=ID:.id,DEST:.destination,RELAY:.relay
```

### Talk to a remote daemon

```sh
punchctl --addr http://10.0.0.5:28854 --token "$PUNCH_TOKEN" status
```

## Configuration

Punch keeps its configuration in a SQLite database (`punch.db`), seeded with sensible defaults on first launch. You change it live via the CLI — no file editing, no restart:

```sh
punchctl config                                 # list everything
punchctl config get dns.listen
punchctl config set api.secret "change-me"
punchctl config set check.full_interval 86400
punchctl config set check.outside_url http://www.gstatic.com/generate_204
punchctl config set check.domestic_url http://connect.rom.miui.com/generate_204
punchctl config set check.interval 10
```

If you need to override a value at startup (for example to bring the daemon up on a different port), use `-s` on `punchd`. The new value is persisted, so it survives restarts:

```sh
sudo punchd -s dns.listen=0.0.0.0:53 -s api.listen=127.0.0.1:8080
sudo punchd -s system.log_level=debug
```

Defaults worth knowing:

| Setting                 | Default                                           |
| ----------------------- | ------------------------------------------------- |
| DNS listener            | `0.0.0.0:28853`                                   |
| API listener            | `127.0.0.1:28854`                                 |
| TUN device              | `punch0`                                          |
| DoH upstreams           | `doh.pub`, `dns.alidns.com`                       |
| Fake IP pool            | `198.18.0.0/15`, `fdfe:dcba:9876::/64`            |
| Outside check URL       | `http://www.gstatic.com/generate_204`             |
| Domestic check URL      | `http://connect.rom.miui.com/generate_204`        |
| Internet/selected check | every 10 seconds                                  |
| Full relay check        | every 86400 seconds                               |
| Session history         | last 1000                                         |

Default data directory:

| Platform           | Directory                                      |
| ------------------ | ---------------------------------------------- |
| macOS              | `~/Library/Application Support/punch`          |
| Linux / other Unix | `$XDG_CONFIG_HOME/punch` or `~/.config/punch`  |
| Windows            | `%APPDATA%\punch` or `~/AppData/Roaming/punch` |

Override with `punchd -data-dir ./data` while developing.

## Relay provider format

Subscription URLs and local provider files use Mihomo-style proxy entries:

```yaml
proxies:
  - name: hk-1
    type: ss
    server: hk-1.relay.example
    port: 8388
    cipher: aes-128-gcm
    password: example

  - name: sg-1
    type: ss
    server: sg-1.relay.example
    port: 8388
    cipher: aes-128-gcm
    password: example
```

Relay hostnames resolve through the normal DNS upstream configuration. For
provider domains that need a specific DNS server, add a domain-scoped upstream,
for example `punchctl dns upstreams create https://some-dns-server --bootstrap 223.5.5.5 --domains sbs`.

Don't commit real relay credentials or private subscription URLs to a repo.

## How it fits together

```text
applications
    |
    v
 system DNS -> Punch DNS :28853 -> rules -> reject | direct | relay
    |                               |
    |                               v
    +-> fake IP pool ---------> Punch TUN engine
                                    |
                                    v
                              relay selector
                                    |
                                    v
                           direct network / proxy relay

punchctl -> Punch API :28854 -> config, DNS, relays, sessions, status
```

Three pieces:

- **`punchd`** — long-running daemon. Owns the TUN device, the DNS server, relay health checks, and the local HTTP API.
- **`punchctl`** — CLI client. Talks to the daemon's API to read state and change configuration.
- **`punch.db`** — SQLite store for all configuration and session history.

## Development

```sh
go build ./...
go test ./...
```

Package layout:

| Path               | Purpose                                              |
| ------------------ | ---------------------------------------------------- |
| `cmd/punchd`       | Daemon entry point                                   |
| `cmd/punchctl`     | Cobra CLI                                            |
| `internal/api`     | Local HTTP API                                       |
| `internal/config`  | SQLite-backed runtime config                         |
| `internal/dns`     | DNS server, rules, cache, resolver integration       |
| `internal/fakeip`  | Fake IP pool                                         |
| `internal/relay`   | Relay groups, providers, health checks, selection    |
| `internal/session` | Session lifecycle and history                        |
| `internal/tun`     | TUN engine and platform route/system DNS integration |

## A note on safety

Punch changes how DNS resolves and how packets are routed on the host it runs on. Try it in a controlled environment first, keep a second terminal open with `punchctl status`, and be ready to stop the daemon if something looks off.
