#!/usr/bin/env bash
# Turns on the ffagent phone app and publishes it to your private Tailscale
# network. Run after deploy/install.sh:
#
#   sudo ./deploy/setup-phone.sh
#
# The app only listens on 127.0.0.1. `tailscale serve` gives it an HTTPS
# address that only devices on your tailnet can reach, and tells the agent
# who each visitor is; the agent only serves the login you enter here.
set -euo pipefail

ETC=/etc/ffagent
CONF=$ETC/config.json
PORT=8787

die() { printf 'setup-phone: %s\n' "$*" >&2; exit 1; }
say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }

[[ $EUID -eq 0 ]] || die "run with sudo"
[[ -f $CONF ]] || die "ffagent isn't installed yet; run ./deploy/install.sh first"
command -v tailscale >/dev/null || die "Tailscale isn't installed. Install it with:
  curl -fsSL https://tailscale.com/install.sh | sh
  sudo tailscale up
then run this again."
tailscale status >/dev/null 2>&1 || die "Tailscale isn't connected. Run 'sudo tailscale up', sign in, then run this again."

say "Who may use the app"
cat <<'TXT'
Enter the login you use for Tailscale: the email shown at the top of
login.tailscale.com (for GitHub sign-ins it looks like username@github).
Requests from anyone else are refused, even on your own tailnet.
TXT
login=${TAILSCALE_LOGIN:-}
[[ -n $login ]] || read -r -p "Tailscale login: " login </dev/tty
login=$(printf '%s' "$login" | tr '[:upper:]' '[:lower:]' | tr -d '[:space:]')
[[ $login =~ ^[a-z0-9._%+-]+@[a-z0-9.-]+$ ]] || die "that doesn't look like a Tailscale login: $login"

say "Turning on the phone app"
cp -p "$CONF" "$CONF.bak"
if grep -q '"web"' "$CONF"; then
  sed -i -e '/"web": {/,/}/ s/"enabled": [a-z]*/"enabled": true/' \
         -e '/"web": {/,/}/ s/"allowed_logins": \[[^]]*\]/"allowed_logins": ["'"$login"'"]/' "$CONF"
else
  # Configs from before the app existed: add the section.
  sed -i '0,/^{/ s/^{/{\n  "web": {\n    "enabled": true,\n    "listen": "127.0.0.1:'"$PORT"'",\n    "allowed_logins": ["'"$login"'"]\n  },\n/' "$CONF"
fi
grep -q '"allowed_logins": \["'"$login"'"\]' "$CONF" || { mv "$CONF.bak" "$CONF"; die "couldn't update $CONF; edit its \"web\" section by hand (see config.example.json)"; }
rm -f "$CONF.bak"
systemctl restart ffagent-watch.service
sleep 2
systemctl is-active --quiet ffagent-watch.service || { journalctl -u ffagent-watch -n 20 --no-pager; die "the agent didn't restart"; }

say "Publishing it to your tailnet with HTTPS"
echo "(If Tailscale asks you to enable HTTPS certificates, open the link it prints, enable them, then run this again.)"
tailscale serve --bg "$PORT"

url=$(tailscale serve status 2>/dev/null | grep -o 'https://[^ ]*' | head -1 || true)
say "Done"
cat <<TXT
On your iPhone:
  1. Install the Tailscale app from the App Store and sign in as $login.
  2. Open Safari and go to:  ${url:-the https:// address shown by 'tailscale serve status'}
  3. Tap the Share button, then "Add to Home Screen".
The app only works while Tailscale is connected on the phone.
To turn it off:  sudo tailscale serve --https=443 off
TXT
