#!/usr/bin/env bash
# Turns on follow-up questions: ask "why this player?" about a proposed move
# in the phone app, or by replying to a proposal email with a question, and
# Claude answers from the agent's own numbers. Run after install.sh:
#
#   ./deploy/macos/setup-ask.sh
#
# Questions send that move's data (your roster, the players involved, the
# matchup and projections) to the Anthropic API. The API key is stored only
# in the encrypted vault.
set -euo pipefail

APP="$HOME/Library/Application Support/ffagent"
BIN="$APP/bin/ffagent"
CTL="$APP/bin/ffagentctl"

die() { printf 'setup-ask: %s\n' "$*" >&2; exit 1; }
say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }

[[ $EUID -ne 0 ]] || die "run as yourself, not with sudo"
[[ -x $BIN && -f $APP/config.json ]] || die "ffagent isn't installed yet; run ./deploy/macos/install.sh first"

if ! "$CTL" secrets check </dev/null 2>/dev/null | grep -Eq 'anthropic_api_key +set'; then
  say "Anthropic API key"
  cat <<'TXT'
Create a key at console.anthropic.com > API keys (name it "ffagent") and
paste it below; input is hidden. Set a monthly spend limit there too. The
agent also caps itself at "max_per_day" questions (40 by default).
TXT
  [[ -r /dev/tty ]] || die "a terminal is needed to enter the key; re-run interactively"
  "$CTL" secrets set anthropic </dev/tty || die "storing the key failed; re-run: ffagentctl secrets set anthropic"
fi

say "Turning on follow-up questions"
FFAGENT_CONFIG="$APP/config.json" "$BIN" config set ask.enabled true >/dev/null
"$CTL" restart >/dev/null

say "Done"
cat <<TXT
Ask about a proposed move in the phone app ("Ask about this move" on its
card) or by replying to the proposal email with your question.
To turn it off: FFAGENT_CONFIG="$APP/config.json" "$BIN" config set ask.enabled false && ffagentctl restart
TXT
