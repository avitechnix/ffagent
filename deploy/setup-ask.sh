#!/usr/bin/env bash
# Turns on follow-up questions: ask "why this player?" about a proposed move
# in the phone app, or by replying to a proposal email with a question, and
# Claude answers from the agent's own numbers. Run after deploy/install.sh:
#
#   sudo ./deploy/setup-ask.sh
#
# Questions send that move's data (your roster, the players involved, the
# matchup and projections) to the Anthropic API. The API key is stored only
# in the encrypted vault.
set -euo pipefail

ETC=/etc/ffagent
CONF=$ETC/config.json
CTL=/usr/local/bin/ffagentctl

die() { printf 'setup-ask: %s\n' "$*" >&2; exit 1; }
say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }

[[ $EUID -eq 0 ]] || die "run with sudo"
[[ -f $CONF && -x $CTL ]] || die "ffagent isn't installed yet; run ./deploy/install.sh first"

if ! "$CTL" secrets check </dev/null 2>/dev/null | grep -Eq 'anthropic_api_key +set'; then
  say "Anthropic API key"
  cat <<'TXT'
Create a key at console.anthropic.com > API keys (name it "ffagent") and
paste it below; input is hidden. Set a monthly spend limit there too. The
agent also caps itself at "max_per_day" questions (40 by default).
TXT
  [[ -r /dev/tty ]] || die "a terminal is needed to enter the key; re-run interactively"
  "$CTL" secrets set anthropic </dev/tty || die "storing the key failed; re-run: sudo ffagentctl secrets set anthropic"
fi

say "Turning on follow-up questions"
cp -p "$CONF" "$CONF.bak"
if grep -q '"ask"' "$CONF"; then
  sed -i '/"ask": {/,/}/ s/"enabled": [a-z]*/"enabled": true/' "$CONF"
else
  # Configs from before questions existed: add the section.
  sed -i '0,/^{/ s/^{/{\n  "ask": {\n    "enabled": true,\n    "model": "claude-opus-5-5",\n    "max_per_day": 40\n  },\n/' "$CONF"
fi
sed -n '/"ask": {/,/}/p' "$CONF" | grep -q '"enabled": true' || { mv "$CONF.bak" "$CONF"; die "couldn't update $CONF; edit its \"ask\" section by hand (see config.example.json)"; }
rm -f "$CONF.bak"
systemctl restart ffagent-watch.service
sleep 2
systemctl is-active --quiet ffagent-watch.service || { journalctl -u ffagent-watch -n 20 --no-pager; die "the agent didn't restart"; }

say "Done"
cat <<'TXT'
Ask about a proposed move:
  - in the phone app: tap "Ask about this move" on its card, or
  - by email: reply to the proposal with your question, e.g.
      "Why drop Smith instead of Jones?"
    (lines with a "?" are treated as questions, never as approve/decline).
Answers explain the agent's numbers; they never approve or decline anything.
To turn it off, set "ask": {"enabled": false} in /etc/ffagent/config.json
and run: sudo systemctl restart ffagent-watch
TXT
