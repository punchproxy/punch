# Punch

<p align="center">
  <strong>A small, programmable proxy stack with a TUN engine, smart DNS, relay selection, and a first-class CLI.</strong>
</p>

<p align="center">
  <img alt="Use at your own risk" src="https://img.shields.io/badge/vibeshit-100%25-ebc034?style=for-the-badge">
  <a href="https://github.com/punchproxy/punch"><img alt="Go version" src="https://img.shields.io/badge/go-1.25+-00ADD8?style=for-the-badge&logo=go&logoColor=white"></a>
  <a href="https://github.com/spf13/cobra"><img alt="CLI" src="https://img.shields.io/badge/cli-cobra-6E40C9?style=for-the-badge"></a>
  <a href="https://github.com/metacubex/mihomo"><img alt="Mihomo powered" src="https://img.shields.io/badge/poweredby-mihomo-FF6B35?style=for-the-badge"></a>
</p>

Punch is a local network proxy daemon designed around three moving pieces:

- `punchd`, the long-running daemon that starts DNS, TUN, relay selection, session tracking, and the local HTTP API.
- `punchctl`, the operator CLI for inspecting and changing a running Punch instance.
- A SQLite-backed runtime store, seeded with sensible defaults on first launch and updated through the API or CLI.

The goal is to make transparent proxying manageable from the command line: DNS rules decide whether traffic should be rejected, relayed, or sent direct; fake IPs give the TUN engine a stable routing target; relay groups can be refreshed, checked, and selected without editing static files.

## What Punch Does

| Area          | Capability                                                                                                       |
| ------------- | ---------------------------------------------------------------------------------------------------------------- |
| DNS           | DoH upstreams, bootstrap resolvers, rule lists, CIDR routes, query tracing, cache inspection, fake IP allocation |
| TUN           | Local TUN device, platform route/system DNS hooks, transparent traffic capture                                   |
| Relays        | Inline or remote relay groups, Mihomo-compatible proxy specs, health checks, latency-aware auto selection        |
| Sessions      | Active and historical session tracking with process, destination, relay, byte counters, and trace data           |
| Control plane | Local HTTP API plus `punchctl` commands with table, JSON, YAML, jsonpath, custom-column, and template output     |

## Architecture

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

## Quick Start

Build both binaries:

```sh
go build -o punchd ./cmd/punchd
go build -o punchctl ./cmd/punchctl
```

Start Punch:

```sh
sudo ./punchd
```

Punch stores `punch.db` in a platform-specific data directory by default. Override it when developing:

```sh
sudo ./punchd -data-dir ./data -debug
```

Check the daemon from another terminal:

```sh
./punchctl status
./punchctl config
./punchctl dns upstreams
./punchctl relaygroups
./punchctl sessions
```

If the API is listening somewhere else or requires a token:

```sh
./punchctl --addr http://127.0.0.1:28854 --token "$PUNCH_TOKEN" status
```

## punchd

`punchd` is the runtime daemon. It opens the local store, loads or seeds configuration, starts DNS, starts relay health checking, brings up the TUN engine, then exposes the API.

```sh
./punchd -version
./punchd -data-dir ./data
./punchd -debug
```

Default listeners:

| Service | Default           |
| ------- | ----------------- |
| DNS     | `0.0.0.0:28853`   |
| API     | `127.0.0.1:28854` |

Default data directory:

| Platform           | Directory                                      |
| ------------------ | ---------------------------------------------- |
| macOS              | `~/Library/Application Support/punch`          |
| Linux / other Unix | `$XDG_CONFIG_HOME/punch` or `~/.config/punch`  |
| Windows            | `%APPDATA%\punch` or `~/AppData/Roaming/punch` |

## punchctl

`punchctl` is the control surface for a live daemon.

```sh
punchctl [--addr URL] [--token TOKEN] [--timeout 5s] COMMAND
```

Common workflows:

```sh
# Runtime health
punchctl status

# Read or update scalar config
punchctl config get dns.listen
punchctl config set relay.auto_strategy.interval 300

# Watch DNS decisions as they happen
punchctl dns trace

# Manage DNS state
punchctl dns upstreams
punchctl dns rules
punchctl dns routes
punchctl dns cache
punchctl dns cache flush
punchctl dns fakeips

# Manage TUN extra routes
punchctl system routes
punchctl system routes create 1.1.1.0/24
punchctl system routes create https://core.telegram.org/resources/cidr.txt
punchctl system routes delete 1.1.1.0/24

# Manage relay groups and relays
punchctl relaygroups create main --url https://example.com/provider.yaml --select auto
punchctl relaygroups refresh main
punchctl relaygroups check main
punchctl relays
punchctl relays select hk-1

# Inspect or terminate sessions
punchctl sessions
punchctl sessions get SESSION_ID
punchctl sessions terminate SESSION_ID
punchctl sessions terminate --all
```

List commands support rich output and filtering:

```sh
punchctl relays -o wide
punchctl relays -o json
punchctl relays --field-selector status=alive --sort-by .latency_ms
punchctl sessions -o custom-columns=ID:.id,DEST:.destination,RELAY:.relay
```

## Configuration Model

Punch persists configuration in `punch.db`, not in a hand-edited YAML file. On first launch it seeds defaults including:

- DNS upstreams: `https://doh.pub/dns-query` and `https://dns.alidns.com/dns-query`
- Fake IP ranges: `198.18.0.0/15` and `fdfe:dcba:9876::/64`
- DNS cache size: `100000`
- TUN device: `punch0`
- Relay selection: `auto`
- Auto-check URL: `https://www.gstatic.com/generate_204`
- Session history limit: `1000`

Scalar keys can be viewed and changed through `punchctl config`:

```sh
punchctl config
punchctl config get api.listen
punchctl config set api.secret "change-me"
```

## Relay Providers

Remote relay groups are useful when you already publish proxy definitions from a subscription endpoint:

```sh
punchctl relaygroups create remote-main \
  --url https://example.com/provider.yaml \
  --select auto \
  --refresh-duration 3600
```

Inline groups are useful for local, explicit proxy specs:

```sh
punchctl relaygroups create lab --provider-file relays.yaml --select manual
punchctl relays create lab --file relays.yaml
```

Provider files use Mihomo-style proxy entries:

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

resolvers:
  - url: https://hk-resolver.example/dns-query
    bootstrap: 223.5.5.5
```

`resolvers` are an optional field and used only for resolving relay `server` hostnames from the provider.

Do not commit real relay credentials or private subscription URLs.

## Development

```sh
go build ./...
go test ./...
```

Useful package map:

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

## Safety Notes

Punch changes networking behavior through DNS, TUN, and route configuration. Run it in a controlled environment first, keep a separate terminal open for `punchctl status`, and document the OS when testing platform-specific behavior.
