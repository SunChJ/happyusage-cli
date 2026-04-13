#!/bin/bash
set -euo pipefail

# ─── Config ───
CODEX_AUTH="${CODEX_HOME:-$HOME/.codex}/auth.json"
CLAUDE_KEYCHAIN_SERVICE="Claude Code-credentials"
CLAUDE_CRED_FILE="$HOME/.claude/.credentials.json"
CODEX_REFRESH_URL="https://auth.openai.com/oauth/token"
CODEX_CLIENT_ID="app_EMoamEEZ73f0CkXaXp7hrann"
CODEX_USAGE_URL="https://chatgpt.com/backend-api/wham/usage"
CLAUDE_USAGE_URL="https://api.anthropic.com/api/oauth/usage"

# ─── Helpers ───
bold()  { printf '\033[1m%s\033[0m' "$*"; }
dim()   { printf '\033[2m%s\033[0m' "$*"; }
green() { printf '\033[32m%s\033[0m' "$*"; }
yellow(){ printf '\033[33m%s\033[0m' "$*"; }
red()   { printf '\033[31m%s\033[0m' "$*"; }

resolve_curl() {
  local candidate
  for candidate in "${HAPPYUSAGE_CURL:-}" /usr/local/opt/curl/bin/curl /usr/bin/curl "$(command -v curl 2>/dev/null || true)"; do
    [ -n "$candidate" ] || continue
    [ -x "$candidate" ] || continue
    "$candidate" --version >/dev/null 2>&1 && {
      printf '%s\n' "$candidate"
      return 0
    }
  done
  return 1
}

CURL_BIN="$(resolve_curl || true)"
CURL_OK=false
[ -n "$CURL_BIN" ] && CURL_OK=true

bar() {
  local pct=$1 width=30
  local filled=$(( pct * width / 100 ))
  local empty=$(( width - filled ))
  # color by remaining: >=30% green, 10-30% yellow, <10% red
  local color="\033[32m"
  (( pct <= 30 )) && color="\033[33m"
  (( pct <= 10 )) && color="\033[31m"
  printf "${color}%${filled}s\033[0m%${empty}s" | tr ' ' '█' | tr ' ' '░'
}

fmt_reset() {
  local ts=$1
  if [ -z "$ts" ] || [ "$ts" = "null" ]; then echo "—"; return; fi
  if command -v python3 &>/dev/null; then
    python3 -c "
from datetime import datetime, timezone
try:
    ts = '$ts'
    if ts.isdigit(): ts = int(ts)
    if isinstance(ts, int):
        dt = datetime.fromtimestamp(ts, tz=timezone.utc)
    else:
        dt = datetime.fromisoformat(ts.replace('Z','+00:00'))
    diff = dt - datetime.now(tz=timezone.utc)
    secs = int(diff.total_seconds())
    if secs < 0: print('已过期'); exit()
    h, r = divmod(secs, 3600)
    m, _ = divmod(r, 60)
    if h > 24:
        d = h // 24
        h = h % 24
        print(f'{d}d {h}h')
    elif h > 0:
        print(f'{h}h {m}m')
    else:
        print(f'{m}m')
except: print('—')
" 2>/dev/null
  else
    echo "$ts"
  fi
}

decode_jwt_payload() {
  local payload
  payload=$(echo "$1" | cut -d. -f2)
  # pad base64
  local pad=$(( 4 - ${#payload} % 4 ))
  (( pad < 4 )) && payload="${payload}$(printf '%0.s=' $(seq 1 $pad))"
  echo "$payload" | base64 -d 2>/dev/null
}

# ─── Claude ───
check_claude() {
  $CURL_OK || { echo "  $(red '✗') curl not available"; return 1; }
  bold "Claude"; echo ""

  # 1) get token: try keychain, fallback to file
  local raw token
  raw=$(security find-generic-password -s "$CLAUDE_KEYCHAIN_SERVICE" -w 2>/dev/null || true)
  if [ -z "$raw" ] && [ -f "$CLAUDE_CRED_FILE" ]; then
    raw=$(cat "$CLAUDE_CRED_FILE")
  fi
  if [ -z "$raw" ]; then
    echo "  $(red '✗') Token not found (no keychain entry or credentials file)"
    return 1
  fi

  token=$(echo "$raw" | python3 -c "import sys,json; print(json.loads(sys.stdin.read())['claudeAiOauth']['accessToken'])" 2>/dev/null || true)
  if [ -z "$token" ]; then
    echo "  $(red '✗') Failed to parse token"
    return 1
  fi

  # 2) fetch usage
  local body
  body=$("$CURL_BIN" -sf "$CLAUDE_USAGE_URL" \
    -H "Authorization: Bearer $token" \
    -H "anthropic-beta: oauth-2025-04-20" 2>/dev/null || true)

  if [ -z "$body" ]; then
    echo "  $(red '✗') API request failed (token may be expired, run 'claude' to re-auth)"
    return 1
  fi

  # 3) parse & display
  local s_pct s_reset w_pct w_reset
  s_pct=$(echo "$body" | jq -r '.five_hour.utilization // empty')
  s_reset=$(echo "$body" | jq -r '.five_hour.resets_at // empty')
  w_pct=$(echo "$body" | jq -r '.seven_day.utilization // empty')
  w_reset=$(echo "$body" | jq -r '.seven_day.resets_at // empty')

  if [ -n "$s_pct" ]; then
    local s_left=$(python3 -c "print(round(100 - $s_pct, 1))")
    local s_left_int=${s_left%.*}
    printf "  Session  $(bar "$s_left_int")  %5s%% left  resets in %s\n" "$s_left" "$(fmt_reset "$s_reset")"
  fi
  if [ -n "$w_pct" ]; then
    local w_left=$(python3 -c "print(round(100 - $w_pct, 1))")
    local w_left_int=${w_left%.*}
    printf "  Weekly   $(bar "$w_left_int")  %5s%% left  resets in %s\n" "$w_left" "$(fmt_reset "$w_reset")"
  fi

  # sonnet separate quota
  local sn_pct sn_reset
  sn_pct=$(echo "$body" | jq -r '.seven_day_sonnet.utilization // empty')
  sn_reset=$(echo "$body" | jq -r '.seven_day_sonnet.resets_at // empty')
  if [ -n "$sn_pct" ]; then
    local sn_left=$(python3 -c "print(round(100 - $sn_pct, 1))")
    local sn_left_int=${sn_left%.*}
    printf "  Sonnet   $(bar "$sn_left_int")  %5s%% left  resets in %s\n" "$sn_left" "$(fmt_reset "$sn_reset")"
  fi

  # extra usage
  local extra_enabled
  extra_enabled=$(echo "$body" | jq -r '.extra_usage.is_enabled // false')
  if [ "$extra_enabled" = "true" ]; then
    local extra_used extra_limit
    extra_used=$(echo "$body" | jq -r '.extra_usage.used_credits // 0')
    extra_limit=$(echo "$body" | jq -r '.extra_usage.monthly_limit // empty')
    if [ -n "$extra_limit" ] && [ "$extra_limit" != "null" ]; then
      printf "  Extra    \$%.2f / \$%.2f\n" "$extra_used" "$extra_limit"
    fi
  fi
}

# ─── Codex ───
check_codex() {
  $CURL_OK || { echo "  $(red '✗') curl not available"; return 1; }
  bold "Codex"; echo ""

  if [ ! -f "$CODEX_AUTH" ]; then
    echo "  $(red '✗') Auth file not found: $CODEX_AUTH"
    return 1
  fi

  local token refresh_token account_id
  token=$(jq -r '.tokens.access_token // empty' "$CODEX_AUTH")
  refresh_token=$(jq -r '.tokens.refresh_token // empty' "$CODEX_AUTH")

  if [ -z "$token" ] && [ -z "$refresh_token" ]; then
    echo "  $(red '✗') No token found. Run 'codex' to authenticate."
    return 1
  fi

  # extract account_id from JWT
  account_id=$(decode_jwt_payload "$token" | jq -r '.["https://api.openai.com/auth"].chatgpt_account_id // empty' 2>/dev/null || true)

  # 1) try fetch
  local body http_code
  http_code=$("$CURL_BIN" -s -o /tmp/_codex_body -w '%{http_code}' "$CODEX_USAGE_URL" \
    -H "Authorization: Bearer $token" \
    ${account_id:+-H "ChatGPT-Account-Id: $account_id"} 2>/dev/null || echo "000")
  body=$(cat /tmp/_codex_body 2>/dev/null || true)

  # 2) if 401, refresh and retry
  if [ "$http_code" = "401" ] && [ -n "$refresh_token" ]; then
    local refresh_resp new_token
    refresh_resp=$("$CURL_BIN" -s -X POST "$CODEX_REFRESH_URL" \
      -H "Content-Type: application/x-www-form-urlencoded" \
      -d "grant_type=refresh_token&client_id=$CODEX_CLIENT_ID&refresh_token=$refresh_token" 2>/dev/null)
    new_token=$(echo "$refresh_resp" | jq -r '.access_token // empty')

    if [ -n "$new_token" ]; then
      # persist refreshed token
      local new_refresh
      new_refresh=$(echo "$refresh_resp" | jq -r '.refresh_token // empty')
      jq --arg at "$new_token" \
         --arg rt "${new_refresh:-$refresh_token}" \
         '.tokens.access_token = $at | .tokens.refresh_token = $rt | .last_refresh = (now | todate)' \
         "$CODEX_AUTH" > /tmp/_codex_auth_new && mv /tmp/_codex_auth_new "$CODEX_AUTH"

      token="$new_token"
      account_id=$(decode_jwt_payload "$token" | jq -r '.["https://api.openai.com/auth"].chatgpt_account_id // empty' 2>/dev/null || true)

      http_code=$("$CURL_BIN" -s -o /tmp/_codex_body -w '%{http_code}' "$CODEX_USAGE_URL" \
        -H "Authorization: Bearer $token" \
        ${account_id:+-H "ChatGPT-Account-Id: $account_id"} 2>/dev/null || echo "000")
      body=$(cat /tmp/_codex_body 2>/dev/null || true)
    fi
  fi

  if [ "$http_code" != "200" ]; then
    echo "  $(red '✗') API returned HTTP $http_code. Run 'codex' to re-auth."
    return 1
  fi

  # 3) parse & display
  local plan
  plan=$(echo "$body" | jq -r '.plan_type // empty')
  if [ -n "$plan" ]; then
    dim "  plan: $plan"; echo ""
  fi

  # primary / secondary windows
  local p_pct p_reset s_pct s_reset
  p_pct=$(echo "$body" | jq -r '.rate_limit.primary_window.used_percent // empty')
  p_reset=$(echo "$body" | jq -r '.rate_limit.primary_window.reset_at // empty')
  s_pct=$(echo "$body" | jq -r '.rate_limit.secondary_window.used_percent // empty')
  s_reset=$(echo "$body" | jq -r '.rate_limit.secondary_window.reset_at // empty')

  if [ -n "$p_pct" ]; then
    local p_left=$(( 100 - p_pct ))
    printf "  Session  $(bar "$p_left")  %4s%% left  resets in %s\n" "$p_left" "$(fmt_reset "$p_reset")"
  fi
  if [ -n "$s_pct" ]; then
    local s_left=$(( 100 - s_pct ))
    printf "  Weekly   $(bar "$s_left")  %4s%% left  resets in %s\n" "$s_left" "$(fmt_reset "$s_reset")"
  fi

  # additional model-specific limits
  local add_count
  add_count=$(echo "$body" | jq -r '.additional_rate_limits | length // 0' 2>/dev/null || echo 0)
  if [ "$add_count" -gt 0 ] 2>/dev/null; then
    echo "$body" | jq -r '.additional_rate_limits[]? | select(.rate_limit.primary_window.used_percent != null) | "\(.limit_name // "Model")|\(.rate_limit.primary_window.used_percent)|\(.rate_limit.primary_window.reset_at // "")"' 2>/dev/null | while IFS='|' read -r name pct reset; do
      local short="${name#GPT-*-Codex-}"
      [ -z "$short" ] && short="$name"
      local m_left=$(( 100 - pct ))
      printf "  %-8s $(bar "$m_left")  %4s%% left  resets in %s\n" "$short" "$m_left" "$(fmt_reset "$reset")"
    done
  fi

  # credits
  local credits
  credits=$(echo "$body" | jq -r '.credits.balance // "0"')
  if [ "$credits" != "0" ] && [ "$credits" != "null" ]; then
    printf "  Credits  %s\n" "$credits"
  fi

  rm -f /tmp/_codex_body /tmp/_codex_auth_new
}

# ─── Main ───
echo ""
check_claude
echo ""
check_codex
echo ""
