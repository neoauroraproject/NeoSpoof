#!/bin/sh
set -eu

repo="neoauroraproject/NeoSpoof"
dest="${PREFIX:-/usr/local/bin}/neospoof"

case "$(uname -m)" in
  x86_64|amd64) asset="neospoof-linux-amd64" ;;
  aarch64|arm64) asset="neospoof-linux-arm64" ;;
  *)
    echo "این معماری پشتیبانی نمی‌شود: $(uname -m)"
    exit 1
    ;;
esac

url="https://github.com/${repo}/releases/latest/download/${asset}"
tmp="$(mktemp)"
curl -fL "$url" -o "$tmp"
install -m 0755 "$tmp" "$dest"
rm -f "$tmp"

echo "نصب شد: $dest"
echo "برای بالا آوردن: sudo $dest"
exec "$dest"
