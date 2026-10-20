#!/usr/bin/env bash
# Installs ffagent on a Mac as a background service that runs as you.
#
#   git clone https://github.com/avitechnix/ffagent
#   cd ffagent && ./deploy/macos/install.sh
#
# No sudo. Re-running it updates the binary and service and keeps your
# config and vault. Optional environment: LEAGUE_ID, TEAM_ID, AGENT_EMAIL,
# NOTIFY_EMAIL, TIME_ZONE (skip the prompts), MODE=apply|dry-run (default:
# ask), FFAGENT_UPDATE=1 (set by `ffagentctl update`: skips the test email).
set -euo pipefail

GO_VERSION=go1.27.2
GO_SHA256_arm64=76812b213b1b2302c978d28fa52fa92d541704b9e7d9d5db8002c50e4018c4c5
GO_SHA256_amd64=587b59182488b23aa6e5fc25110405a3e0e5b38ed2f5b2f46ed13c32aee356fe

APP="$HOME/Library/Application Support/ffagent"
BIN="$APP/bin/ffagent"
CTL="$APP/bin/ffagentctl"
CONF="$APP/config.json"

say()  { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
die()  { printf 'install: %s\n' "$*" >&2; exit 1; }
ask()  { local r; read -r -p "$1" r </dev/tty; printf '%s' "$r"; }
have_tty() { [[ -r /dev/tty && -w /dev/tty ]] && { : </dev/tty; } 2>/dev/null; }
cfg()  { FFAGENT_CONFIG="$CONF" "$BIN" config set "$@" >/dev/null; }

SRC=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
[[ -f $SRC/go.mod && -d $SRC/cmd/ffagent ]] || die "run this from the ffagent source tree"

# --- preflight --------------------------------------------------------------
[[ $(uname -s) == Darwin ]] || die "this installer is for macOS; on Linux use ./deploy/install.sh"
[[ $EUID -ne 0 ]] || die "run as yourself, not with sudo: the service runs in your account"
case $(uname -m) in
  arm64) ARCH=arm64; GO_SHA=$GO_SHA256_arm64 ;;
  x86_64) ARCH=amd64; GO_SHA=$GO_SHA256_amd64 ;;
  *) die "unsupported CPU $(uname -m)" ;;
esac
for t in curl shasum tar launchctl plutil security; do command -v $t >/dev/null || die "$t is required"; done
install -d -m 0700 "$APP" "$APP/bin"

# --- Go toolchain -------------------------------------------------------------
# Any Go >= 1.21 works (e.g. from Homebrew): go.mod pins a patched toolchain,
# which the go command downloads and verifies against the Go checksum database.
GO=$(command -v go || true)
if [[ -n $GO ]] && ! "$GO" version | grep -Eq 'go1\.(2[1-9]|[3-9][0-9])'; then GO=""; fi
if [[ -z $GO && -x $APP/go/bin/go ]]; then GO=$APP/go/bin/go; fi
if [[ -z $GO ]]; then
  say "Installing $GO_VERSION for ffagent's use (checksum-verified)"
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  tgz=$GO_VERSION.darwin-$ARCH.tar.gz
  curl -fsSL --proto '=https' --tlsv1.2 -o "$tmp/$tgz" "https://go.dev/dl/$tgz"
  echo "$GO_SHA  $tmp/$tgz" | shasum -a 256 -c --quiet - || die "Go download failed checksum verification"
  rm -rf "$APP/go" && tar -C "$APP" -xzf "$tmp/$tgz"
  GO=$APP/go/bin/go
fi

# --- build ------------------------------------------------------------------
say "Building ffagent"
build=$(mktemp -d)
(cd "$SRC" && GOTOOLCHAIN=auto GOPATH=$build/gopath GOCACHE=$build/cache GOFLAGS=-mod=readonly \
  "$GO" build -trimpath -o "$build/ffagent" ./cmd/ffagent)
install -m 0755 "$build/ffagent" "$BIN"
install -m 0755 "$SRC/deploy/macos/ffagentctl" "$CTL"
chmod -R u+w "$build" && rm -rf "$build"
printf '%s\n' "$SRC" > "$APP/source"

# Put ffagentctl on your PATH.
LINK=""
for d in /opt/homebrew/bin /usr/local/bin "$HOME/.local/bin"; do
  if [[ $d == "$HOME/.local/bin" ]]; then mkdir -p "$d"; fi
  if [[ -d $d && -w $d ]]; then ln -sf "$CTL" "$d/ffagentctl"; LINK=$d; break; fi
done
case ":$PATH:" in
  *":$LINK:"*) ;;
  *) [[ -n $LINK ]] && echo "Note: add $LINK to your PATH to run ffagentctl, e.g.: echo 'export PATH=\"$LINK:\$PATH\"' >> ~/.zshrc" ;;
esac

# --- config -------------------------------------------------------------------
if [[ ! -f $CONF ]]; then
  say "League settings"
  league=${LEAGUE_ID:-}
  [[ -n $league ]] || { have_tty && league=$(ask "ESPN league ID (the leagueId= number in your league URL): "); }
  [[ $league =~ ^[0-9]+$ ]] || die "league ID must be a number"
  team=${TEAM_ID:-}
  [[ -n $team ]] || { have_tty && team=$(ask "Your team ID (Enter to detect it from your login): "); }
  team=${team:-0}
  [[ $team =~ ^[0-9]+$ ]] || die "team ID must be a number"
  season=$(date +%Y); (( 10#$(date +%m) < 3 )) && season=$((season - 1))
  install -m 0600 "$SRC/config.example.json" "$CONF"
  cfg league_id "$league"; cfg season "$season"; cfg team_id "$team"; cfg protected_players '[]'
  echo "Wrote $CONF (edit it to protect players or tune behavior)"
fi

# --- email for approvals and summaries ------------------------------------------
email_re='^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$'
if grep -q '"notify_to": "you@example.com"' "$CONF" || ! grep -q '"notify_to"' "$CONF"; then
  say "Email: the agent asks before every move"
  cat <<'TXT'
The agent emails you each move with its rationale and waits for your reply
("approve all", "approve 1 3", "decline 2"). It sends from, and reads your
replies in, a Gmail account of its own. Use a NEW Gmail account just for the
agent (e.g. yourname.ffagent@gmail.com): its app password grants full access
to that mailbox, so it shouldn't be your personal one.
TXT
  agent_email=${AGENT_EMAIL:-}
  [[ -n $agent_email ]] || { have_tty && agent_email=$(ask "Agent's Gmail address: "); }
  [[ $agent_email =~ $email_re ]] || die "not an email address: $agent_email"
  notify=${NOTIFY_EMAIL:-}
  [[ -n $notify ]] || { have_tty && notify=$(ask "Your email address (proposals go here; only replies from it count): "); }
  [[ $notify =~ $email_re ]] || die "not an email address: $notify"
  tz=${TIME_ZONE:-}
  if [[ -z $tz ]]; then
    guess=$(readlink /etc/localtime 2>/dev/null | sed 's|.*/zoneinfo/||'); guess=${guess:-America/New_York}
    have_tty && tz=$(ask "Your time zone [$guess]: ")
    tz=${tz:-$guess}
  fi
  [[ $tz =~ ^[A-Za-z_]+(/[A-Za-z0-9_+-]+)*$ ]] || die "not a time zone: $tz"
  cfg email.address "$agent_email"; cfg email.notify_to "$notify"; cfg email.time_zone "$tz"
  NEED_EMAIL_SECRET=1
fi

# --- vault passphrase: random, never displayed, kept in your Keychain -------------
say "Vault passphrase (a random one, stored in your login Keychain)"
FFAGENT_KEYCHAIN=ffagent "$BIN" secrets keychain-init </dev/null ||
  die "couldn't use the Keychain. Over SSH, unlock it first: security unlock-keychain ~/Library/Keychains/login.keychain-db"

# --- ESPN cookies -------------------------------------------------------------
if [[ ! -f $APP/secrets.vault ]]; then
  say "ESPN login cookies"
  cat <<'TXT'
In a browser where you're logged in to espn.com, open developer tools:
  Safari:      Develop > Show Web Inspector > Storage > Cookies
               (turn on the Develop menu in Safari > Settings > Advanced)
  Chrome/Edge: Application > Cookies > https://www.espn.com
  Firefox:     Storage > Cookies > https://www.espn.com
Copy the values of "espn_s2" and "SWID". Paste them below; input is hidden
and they're stored only in the encrypted vault.
TXT
  have_tty || die "a terminal is needed to enter the cookies; re-run interactively"
  "$CTL" secrets set </dev/tty || die "storing the cookies failed; re-run: ffagentctl secrets set"
fi

if [[ -n ${NEED_EMAIL_SECRET:-} ]] || ! "$CTL" secrets check </dev/null 2>/dev/null | grep -Eq 'email_password +set'; then
  say "Gmail app password for the agent's account"
  cat <<'TXT'
In the agent's Gmail account:
  1. Turn on 2-Step Verification (myaccount.google.com/security).
  2. Create an app password at myaccount.google.com/apppasswords
     (name it "ffagent"). Google shows 16 letters; paste them below.
It's stored only in the encrypted vault.
TXT
  have_tty || die "a terminal is needed to enter the app password; re-run interactively"
  "$CTL" secrets set email </dev/tty || die "storing the app password failed; re-run: ffagentctl secrets set email"
fi

if [[ -z ${FFAGENT_UPDATE:-} ]]; then
  say "Sending a test email"
  "$CTL" email-test </dev/null || die "email isn't working yet; fix the issue above, then re-run this installer (or: ffagentctl email-test)"
fi

# --- verify ------------------------------------------------------------------
say "Checking the connection to your league (read-only)"
if ! "$CTL" matchup </dev/null; then
  die "couldn't read your league. Fix the issue above (often the cookies: ffagentctl secrets set), then re-run this installer."
fi

# --- start --------------------------------------------------------------------
mode=${MODE:-}
if [[ -z $mode ]]; then
  cur=$(cat "$APP/mode" 2>/dev/null || echo dry-run)
  if have_tty; then
    echo
    echo "Start the watcher in:"
    echo "  approve mode: it emails you each move with its rationale and runs only the ones"
    echo "                you approve by replying (nothing happens without your OK), or"
    echo "  dry-run mode: it only logs what it would propose (ffagentctl logs)."
    r=$(ask "Start in approve mode now? [Y/n] (current: $cur): ")
    [[ -z $r ]] && r=y
    [[ $r =~ ^[Yy] ]] && mode=apply || mode=dry-run
  else
    mode=$cur
  fi
fi
[[ $mode == apply || $mode == dry-run ]] || die "MODE must be apply or dry-run"
"$CTL" mode "$mode" >/dev/null

# --- keep the Mac awake -----------------------------------------------------------
sleep_min=$(pmset -g 2>/dev/null | awk '$1 == "sleep" {print $2}')
if [[ -n $sleep_min && $sleep_min != 0 ]]; then
  say "Your Mac sleeps after $sleep_min minutes"
  cat <<'TXT'
The agent can't watch for news while the Mac sleeps. To keep it awake and
have it restart after a power cut, run once:
  sudo pmset -a sleep 0 autorestart 1
(or System Settings > Energy: "Prevent automatic sleeping" and
"Start up automatically after a power failure").
TXT
fi

label=$([[ $mode == apply ]] && echo "approve (emails every move to you first)" || echo "dry-run")
say "Done. The watcher is running in $label mode and starts when you log in."
cat <<TXT
  ffagentctl logs -f        follow what it sees and does
  ffagentctl status         is it running?
  ffagentctl analyze        roster report (also: matchup, lineup, waivers, trades)
  ffagentctl mode apply     email you moves to approve (or: mode dry-run)
  ffagentctl email-test     check email sending and reply reading
  ffagentctl update         update to the latest version (or: auto-update on)
  ffagentctl secrets set    refresh the ESPN cookies when they expire
  $SRC/deploy/macos/setup-phone.sh  turn on the iPhone app (over Tailscale)
  $SRC/deploy/macos/setup-ask.sh    turn on follow-up questions answered by Claude
  $SRC/deploy/macos/uninstall.sh    remove everything
TXT
