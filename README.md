> [!NOTE]
>
> **This repository has moved to [devops-foz/cloudflare-ddns](https://github.com/devops-foz/cloudflare-ddns).**
>
> Archived for history only — new development, releases, and issues happen in the DevOps Foz organization.

# Cloudflare DDNS

A tiny, dependency-free [Cloudflare](https://www.cloudflare.com/) Dynamic DNS
(DDNS) client that runs as a single static Go binary in a `scratch` Docker
image (~9 MB).

It detects changes to your public IPv4 address and automatically updates the
DNS-only `A` records you configure, so services that need a real public IP
(e.g. WireGuard VPN, TeamSpeak, game servers, self-hosted HTTP) stay reachable
even when your ISP rotates your IP address.

## Why this over other tools?

- **Tiny image** — static binary on `scratch`, no shell, no runtime, no
  dependencies (~9 MB, a few MB of RAM).
- **No Account ID or Zone ID required** — zones are resolved automatically
  from the record names.
- **Minimal API usage** — writes to Cloudflare only when the IP actually
  changes; a persistent cache avoids redundant updates across restarts.
- **Secure by default** — non-root user, read-only root filesystem, healthcheck
  built in.
- **Simple config** — everything is driven by environment variables.

## How it works

1. Fetch the current public IPv4 from an ordered list of echo providers (with
   automatic fallback).
2. Compare it against the last applied IP stored in a cache file.
3. If it changed, resolve each record's zone and `PATCH` the DNS records via
   the Cloudflare API v4.
4. Persist the new IP and sleep until the next check.

## Requirements

- Docker with the Compose plugin (`docker compose`) — or the legacy
  `docker-compose`.
- A Cloudflare API token with the **Zone → DNS → Edit** permission.

### Creating the API token

1. Go to <https://dash.cloudflare.com/profile/api-tokens> → **Create Token**.
2. Use the **"Edit zone DNS"** template (or create a custom token).
3. For **Zone Resources**, pick **Include → Specific zone →** your domain
   (or **All zones**).
4. Copy the token — it is shown only once.

> Note: this token is enough. You do **not** need your Account ID or Global
> API Key.

## Quick start

```bash
git clone https://github.com/kolpzs/cloudflare-ddns.git
cd cloudflare-ddns

cp .env.example .env
# edit .env and fill in CLOUDFLARE_API_TOKEN and CF_RECORDS

docker compose up -d --build
docker compose logs -f cloudflare-ddns
```

Expected output on first run:

```
detected public IP 203.0.113.10 via https://api.cloudflare.com/cdn-cgi/trace
IP changed:  -> 203.0.113.10
zone example.com -> 0b1f2c...
updated home.example.com -> 203.0.113.10 (ttl=120s)
```

After that, each cycle logs `IP unchanged` until your ISP assigns a new IP.

## Configuration

All settings are read from environment variables (via the `.env` file when
using Docker Compose).

| Variable                 | Required | Default                                   | Description                                                        |
| ------------------------ | :------: | ----------------------------------------- | ------------------------------------------------------------------ |
| `CLOUDFLARE_API_TOKEN`   | yes      | —                                         | Cloudflare API token (Zone → DNS → Edit).                          |
| `CF_RECORDS`             | yes      | —                                         | Comma-separated FQDNs of the `A` records to keep in sync.          |
| `CF_TTL`                 | no       | `120`                                     | DNS TTL in seconds (Cloudflare minimum is 120).                    |
| `CHECK_INTERVAL`         | no       | `300`                                     | Seconds between checks (300 = 5 min).                              |
| `CF_IP_PROVIDERS`        | no       | built-in list                             | Comma-separated URLs to override the IP echo providers.            |

Built-in IP providers (tried in order):

1. `https://api.cloudflare.com/cdn-cgi/trace`
2. `https://api.ipify.org`
3. `https://checkip.amazonaws.com`
4. `https://ifconfig.me/ip`

## Running without Docker

Requires Go 1.22+ (builds a fully static, CGO-disabled binary).

```bash
go build -o ddns .

export CLOUDFLARE_API_TOKEN=your-token
export CF_RECORDS=home.example.com,vpn.example.com
export CACHE_FILE=/tmp/cfddns/ip.cache
export HEARTBEAT_FILE=/tmp/cfddns/heartbeat

./ddns
```

## Notes

- Records are updated as **DNS-only** (`proxied: false`). If you want records
  proxied through Cloudflare's orange cloud, edit `main.go` (the `proxied`
  field in `updateRecord`) — but note proxied records do not expose your raw
  public IP to clients.
- Only **IPv4** is supported. IPv6 is out of scope for now.
- If you use **Cloudflare Tunnel** (`cloudflared`) instead, you generally do
  not need DDNS at all — the tunnel connects outbound regardless of your IP.
  This tool is for services that must be reached directly on a public IP.

## License

[MIT](LICENSE)
