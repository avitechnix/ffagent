#!/usr/bin/env bash
# Removes the ffagent service, binary, config, encrypted vault, its Keychain
# passphrase, state and logs from this Mac.
set -euo pipefail
[[ $EUID -ne 0 ]] || { echo "run as yourself, not with sudo" >&2; exit 1; }
read -r -p "Remove ffagent, its config, encrypted vault, Keychain passphrase, state and logs? [y/N] " r </dev/tty
[[ $r =~ ^[Yy] ]] || exit 0
DOMAIN="gui/$(id -u)"
for label in com.github.avitechnix.ffagent.watch com.github.avitechnix.ffagent.update; do
  launchctl bootout "$DOMAIN/$label" 2>/dev/null || true
  rm -f "$HOME/Library/LaunchAgents/$label.plist"
done
security delete-generic-password -s ffagent -a vault-passphrase >/dev/null 2>&1 || true
for d in /opt/homebrew/bin /usr/local/bin "$HOME/.local/bin"; do
  [[ -L $d/ffagentctl ]] && rm -f "$d/ffagentctl"
done
rm -rf "$HOME/Library/Application Support/ffagent" "$HOME/Library/Logs/ffagent"
echo "ffagent removed. (If you published the phone app: tailscale serve --https=443 off)"
