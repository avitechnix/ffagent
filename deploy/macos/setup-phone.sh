#!/usr/bin/env bash
# Turns on the ffagent phone app and publishes it to your private Tailscale
# network. Run after deploy/macos/install.sh:
#
#   ./deploy/macos/setup-phone.sh
#
# The app only listens on 127.0.0.1. `tailscale serve` gives it an HTTPS
# address that only devices on your tailnet can reach, and tells the agent
# who each visitor is; the agent only serves the login you enter here.
set -euo pipefail

APP="$HOME/Library/Application Support/ffagent"
BIN="$APP/bin/ffagent"
CTL="$APP/bin/ffagentctl"
PORT=8787

die() { printf 'setup-phone: %s\n' "$*" >&2; exit 1; }
say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
cfg() { FFAGENT_CONFIG="$APP/config.json" "$BIN" config set "$@" >/dev/null; }

[[ $EUID -ne 0 ]] || die "run as yourself, not with sudo"
[[ -x $BIN && -f $APP/config.json ]] || die "ffagent isn't installed yet; run ./deploy/macos/install.sh first"

TS=$(command -v tailscale || true)
[[ -n $TS ]] || { [[ -x /Applications/Tailscale.app/Contents/MacOS/Tailscale ]] && TS=/Applications/Tailscale.app/Contents/MacOS/Tailscale; }
[[ -n $TS ]] || die "Tailscale isn't installed. Install the Tailscale app (tailscale.com/download/mac or the
Mac App Store), sign in, then run this again."
"$TS" status >/dev/null 2>&1 || die "Tailscale isn't connected. Open the Tailscale app, sign in, then run this again."

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
cfg web.enabled true
cfg web.listen "127.0.0.1:$PORT"
cfg web.allowed_logins "[\"$login\"]"
"$CTL" restart >/dev/null

say "Publishing it to your tailnet with HTTPS"
echo "(If Tailscale asks you to enable HTTPS certificates, open the link it prints, enable them, then run this again.)"
"$TS" serve --bg "$PORT"

url=$("$TS" serve status 2>/dev/null | grep -o 'https://[^ ]*' | head -1 || true)
say "Done"
cat <<TXT
On your iPhone:
  1. Install the Tailscale app from the App Store and sign in as $login.
  2. Open Safari and go to:  ${url:-the https:// address shown by 'tailscale serve status'}
  3. Tap the Share button, then "Add to Home Screen".
The app only works while Tailscale is connected on the phone and the Mac.
To turn it off:  $TS serve --https=443 off
TXT
