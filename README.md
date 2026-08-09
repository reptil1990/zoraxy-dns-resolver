# zoraxy-dns-resolver

A [Zoraxy](https://github.com/tobychui/zoraxy) plugin that provides a **split-horizon DNS
resolver**: it answers your internal hostnames locally and forwards everything else to an
upstream resolver (for example an AdGuard Home / Pi-hole instance that does ad blocking).

## How it works

```
LAN client → DNS Resolver ── internal host?  ──► answer directly (→ Zoraxy LAN IP / static IP)
                          └─ no match ─────────► forward to upstream (AdGuard → blocking + internet)
```

The plugin itself does **not** block ads — that is the upstream resolver's job. It exists to
resolve internal services directly so a LAN client reaching a proxied service is sent straight
to Zoraxy.

## Features

- Split-horizon resolution over UDP and TCP
- **Auto-sync**: internal records are pulled from Zoraxy's HTTP reverse proxy rules and resolved
  to a configurable Zoraxy LAN IP — no double bookkeeping
- Static records (`hostname → IP`), including wildcards (`*.intern.example.com`)
- Forwards all other queries to configurable upstream resolvers (e.g. AdGuard)
- In-memory response cache honoring record TTLs
- Live query statistics (total, local, cached, forwarded, top clients)

Known internal hosts are answered authoritatively; a query for a known host with a mismatched
type (e.g. AAAA for an IPv4-only record) returns NODATA rather than leaking to the upstream.

## Configuration

All settings are managed in the plugin UI (**Plugins → DNS Resolver**) and persisted to
`config.json` next to the plugin binary.

| Setting | Default | Description |
|---|---|---|
| DNS Port | `53` | UDP/TCP port the resolver listens on (changing it restarts the resolver) |
| Upstreams | `1.1.1.1:53` | Upstream resolvers, tried in order — **set this to your AdGuard IP** |
| Auto-sync | `on` | Pull internal hostnames from Zoraxy's reverse proxy rules |
| Zoraxy LAN IP | auto | Address that auto-synced hosts resolve to. Leave empty to auto-detect the host's LAN IP (the plugin always runs on the Zoraxy host) |
| Static Records | — | Manual `hostname → IP` entries, wildcards supported |

### Typical setup

1. Set **Upstreams** to your AdGuard Home instance, e.g. `192.168.1.5:53`.
2. Enable **Auto-sync**. Leave **Zoraxy LAN IP** empty to auto-detect the host IP, or set it explicitly.
3. Point your router's DHCP DNS (or the clients) at the Zoraxy host.

Now `service.intern.example.com` resolves to Zoraxy directly, and `example.org` is resolved
(and ad-filtered) by AdGuard.

### Port 53 is privileged

Binding UDP/TCP port `53` requires elevated privileges. Either run Zoraxy as root, grant the
plugin binary the capability:

```bash
setcap 'cap_net_bind_service=+ep' zoraxy-dns-resolver
```

or set a non-privileged port (e.g. `5300`) in the UI and redirect `53 → 5300` with your firewall.

## Installation

1. Download the binary for your platform from the releases page.
2. Create a folder in your Zoraxy plugins directory with the **same name as the binary**:
   ```
   plugins/
   └── zoraxy-dns-resolver/
       └── zoraxy-dns-resolver        # Linux binary (no extension)
   ```
3. Make it executable: `chmod +x zoraxy-dns-resolver`
4. In the Zoraxy web UI: **Plugins → Reload → Enable**

## Building from source

```bash
go build -o zoraxy-dns-resolver .
```
