#!/bin/sh
# NeoSpoof one-line installer — downloads the prebuilt binary and runs it as a
# systemd service.  Usage:
#   bash <(curl -Ls https://raw.githubusercontent.com/neoauroraproject/NeoSpoof/main/install.sh)
set -eu

repo="neoauroraproject/NeoSpoof"
dest="/usr/local/bin/neospoof"
datadir="/var/lib/neospoof"

if [ "$(id -u)" != 0 ]; then
  echo "لطفاً با root اجرا کنید (sudo)."
  exit 1
fi

case "$(uname -m)" in
  x86_64|amd64)  asset="neospoof-linux-amd64" ;;
  aarch64|arm64) asset="neospoof-linux-arm64" ;;
  *) echo "این معماری پشتیبانی نمی‌شود: $(uname -m)"; exit 1 ;;
esac

url="https://github.com/${repo}/releases/latest/download/${asset}"
echo "در حال دانلود $asset ..."
tmp="$(mktemp)"
if ! curl -fL "$url" -o "$tmp"; then
  echo "دانلود باینری ناموفق بود. آیا یک Release منتشر شده؟ ($url)"
  rm -f "$tmp"; exit 1
fi
install -m 0755 "$tmp" "$dest"
rm -f "$tmp"
mkdir -p "$datadir"
echo "نصب شد: $dest"

if command -v systemctl >/dev/null 2>&1; then
  cat >/etc/systemd/system/neospoof.service <<UNIT
[Unit]
Description=NeoSpoof panel
After=network.target

[Service]
ExecStart=$dest -data $datadir ${NEOSPOOF_PORT:+-port $NEOSPOOF_PORT}
WorkingDirectory=$datadir
Restart=on-failure

[Install]
WantedBy=multi-user.target
UNIT
  systemctl daemon-reload
  systemctl enable --now neospoof
  sleep 2
  echo ""
  echo "──────────── آدرس و رمز ورود ────────────"
  journalctl -u neospoof -n 40 --no-pager 2>/dev/null | grep -E "آدرس|کاربری|رمز|─" || \
    echo "برای دیدن آدرس و رمز: journalctl -u neospoof -n 40"
  echo "─────────────────────────────────────────"
  echo "لاگ زنده: journalctl -u neospoof -f"
else
  echo "systemd نیست؛ دستی اجرا کنید: sudo $dest -data $datadir"
fi

echo ""
echo "برای «فعال‌سازی تانل» به باینری هسته‌ی اسپوف نیاز است؛ مسیرش را با فلگ -core بدهید."
