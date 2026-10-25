# NeoSpoof

> ⚠️ For authorized security testing and research only. IP spoofing may be
> illegal without permission; you are responsible for complying with the law.

**NeoSpoof** is a single-binary, licensed control panel for IP-spoofing tunnels:

1. **Tests** which spoofed source IPs survive the path between two servers, and
2. **Activates** real tunnels from the passing IPs — one per location, each on
   its own local **SOCKS5 port** — by running the spoof core as a subprocess.

## Quick install (one line)

```bash
bash <(curl -Ls https://raw.githubusercontent.com/neoauroraproject/NeoSpoof/main/install.sh)
```

Downloads the prebuilt binary, installs it to `/usr/local/bin/neospoof`, runs it
as a systemd service, and prints the panel URL + one-time password. Set
`NEOSPOOF_PORT` to pin a fixed port. Linux (amd64/arm64), root required.
Prebuilt binaries live on the [Releases](https://github.com/neoauroraproject/NeoSpoof/releases) page.

## Build from source

```bash
go build -o neospoof .
sudo ./neospoof
```

Flags: `-data` (state dir), `-addr`, `-port` (0 = random), `-core` (spoof core
binary each tunnel runs), `-license-proxy`, `-reset-password`.

## How it combines two projects

- **Candy-Spoof** (MIT) — self-contained bidirectional spoof tunnel with in-core
  SOCKS5; NeoSpoof spawns it per location and generates its config from the
  passing spoof IPs.
- **spoof-tunnel** (Apache-2.0, ParsaKSH) — the multi-transport / multi-tunnel
  manager model; its core can be plugged in via `-core`.

## Licensing

NeoSpoof verifies **Ed25519-signed, short-lived tokens** (public key embedded),
binds a license to the server's IP (can't be moved to another server), re-checks
every 24h trusting the server's signed time, and deactivates when it can no
longer validate. Server contract + Cloudflare Worker reference in
[`docs/LICENSE-SERVER.md`](docs/LICENSE-SERVER.md). The signing private key is
never in this repo.

> Software running on the customer's own server can always be patched; this is
> the strongest *practical* protection, not literal uncrackability.

## License

Builds on Candy-Spoof (MIT) and spoof-tunnel (Apache-2.0); keep their notices.
