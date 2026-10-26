#!/bin/sh
# NeoSpoof one-line installer — downloads the prebuilt binary and (re)starts it
# as a systemd service. Re-running it upgrades in place: the new binary replaces
# the old one and the service is restarted onto it, while the data dir (password,
# settings, license, tunnels) is kept untouched. Usage:
#   bash <(curl -Ls https://raw.githubusercontent.com/neoauroraproject/NeoSpoof/main/install.sh)
set -eu

repo="neoauroraproject/NeoSpoof"
dest="/usr/local/bin/neospoof"
datadir="/var/lib/neospoof"

if [ "$(id -u)" != 0 ]; then
  echo "Please run as root (sudo)."
  exit 1
fi

case "$(uname -m)" in
  x86_64|amd64)  asset="neospoof-linux-amd64" ;;
  aarch64|arm64) asset="neospoof-linux-arm64" ;;
  *) echo "Unsupported architecture: $(uname -m)"; exit 1 ;;
esac

url="https://github.com/${repo}/releases/latest/download/${asset}"
echo "Downloading ${asset} ..."
tmp="$(mktemp)"
if ! curl -fL "$url" -o "$tmp"; then
  echo "Binary download failed. Has a release been published? (${url})"
  rm -f "$tmp"; exit 1
fi
install -m 0755 "$tmp" "$dest"
rm -f "$tmp"
mkdir -p "$datadir"
echo "Installed: ${dest}"

# Spoof core binaries (client + server). The panel itself is only the
# orchestrator — it runs these to actually bring tunnels up. Prebuilt for amd64.
coreflags=""
case "$(uname -m)" in
  x86_64|amd64)
    for c in client server; do
      if curl -fL "https://github.com/${repo}/releases/latest/download/neospoof-${c}-linux-amd64" -o "/usr/local/bin/neospoof-${c}"; then
        chmod 0755 "/usr/local/bin/neospoof-${c}"
        echo "Installed core: /usr/local/bin/neospoof-${c}"
      else
        echo "Warning: could not download the ${c} core (is it in the latest release?)."
      fi
    done
    if [ -x /usr/local/bin/neospoof-client ] && [ -x /usr/local/bin/neospoof-server ]; then
      coreflags="-core /usr/local/bin/neospoof-client -server-core /usr/local/bin/neospoof-server"
    fi
    ;;
  *)
    echo "Note: spoof cores are prebuilt only for amd64; build them with cargo on this arch to enable tunnels."
    ;;
esac

if command -v systemctl >/dev/null 2>&1; then
  cat >/etc/systemd/system/neospoof.service <<UNIT
[Unit]
Description=NeoSpoof panel
After=network.target

[Service]
ExecStart=${dest} -data ${datadir} ${NEOSPOOF_PORT:+-port ${NEOSPOOF_PORT}} ${coreflags}
WorkingDirectory=${datadir}
Restart=on-failure

[Install]
WantedBy=multi-user.target
UNIT
  systemctl daemon-reload
  systemctl enable neospoof >/dev/null 2>&1 || true
  # Restart (not just "enable --now") so a re-install actually swaps the running
  # process over to the freshly downloaded binary instead of leaving the old
  # version running.
  systemctl restart neospoof
  sleep 2
  echo ""
  echo "──────────── Login details ────────────"
  journalctl -u neospoof -n 40 --no-pager 2>/dev/null | grep -E "URL|Username|Password|version|─" || \
    echo "To see the URL and password: journalctl -u neospoof -n 40"
  echo "────────────────────────────────────────"
  echo "Live logs: journalctl -u neospoof -f"
else
  echo "systemd not found; run manually: sudo ${dest} -data ${datadir}"
fi

echo ""
if [ -n "$coreflags" ]; then
  echo "Spoof cores installed and wired; tunnels can be activated from the panel."
else
  echo "Spoof cores not installed for this arch; build client/server with cargo and pass -core / -server-core."
fi
