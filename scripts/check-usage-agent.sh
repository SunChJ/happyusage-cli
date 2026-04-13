#!/bin/bash
set -euo pipefail

# ─────────────────────────────────────────────────────────
# check-usage-agent.sh
# LLM agent 友好版：输出纯 JSON，无 ANSI，无交互
#
# 支持: claude, codex, cursor, copilot, gemini, windsurf
#
# 用法:
#   ./check-usage-agent.sh                        # 全部
#   ./check-usage-agent.sh claude codex            # 指定多个
#   ./check-usage-agent.sh --oneline cursor        # 单行 + 指定
#   ./check-usage-agent.sh --oneline               # 单行全部
#   ./check-usage-agent.sh --raw                   # 原始 JSON
# ─────────────────────────────────────────────────────────

ALL_PROVIDERS="claude codex cursor copilot gemini windsurf"

# ─── Shared helpers ───
ts_now() { python3 -c "from datetime import datetime,timezone; print(datetime.now(timezone.utc).isoformat())"; }

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

decode_jwt_payload() {
  local payload
  payload=$(echo "$1" | cut -d. -f2)
  local pad=$(( 4 - ${#payload} % 4 ))
  (( pad < 4 )) && payload="${payload}$(printf '%0.s=' $(seq 1 $pad))"
  echo "$payload" | base64 -d 2>/dev/null
}

fail_json() { echo "{\"provider\":\"$1\",\"ok\":false,\"error\":\"$2\"}"; }

# ════════════════════════════════════════════════════════════
#  CLAUDE
# ════════════════════════════════════════════════════════════
check_claude_json() {
  $CURL_OK || { fail_json claude "curl not available"; return; }
  local raw token
  raw=$(security find-generic-password -s "Claude Code-credentials" -w 2>/dev/null || true)
  [ -z "$raw" ] && [ -f "$HOME/.claude/.credentials.json" ] && raw=$(cat "$HOME/.claude/.credentials.json")
  [ -z "$raw" ] && { fail_json claude "no credentials found"; return; }

  token=$(echo "$raw" | python3 -c "import sys,json; print(json.loads(sys.stdin.read())['claudeAiOauth']['accessToken'])" 2>/dev/null || true)
  [ -z "$token" ] && { fail_json claude "failed to parse token"; return; }

  local body
  body=$("$CURL_BIN" -sf "https://api.anthropic.com/api/oauth/usage" \
    -H "Authorization: Bearer $token" \
    -H "anthropic-beta: oauth-2025-04-20" 2>/dev/null || true)
  [ -z "$body" ] && { fail_json claude "api failed, token may be expired"; return; }

  local now; now=$(ts_now)
  echo "$body" | python3 -c "
import json, sys
raw = json.load(sys.stdin)
now = sys.argv[1]
quotas = []
for key, label, period in [
    ('five_hour','session','5h'), ('seven_day','weekly','7d'),
    ('seven_day_sonnet','sonnet_weekly','7d'), ('seven_day_opus','opus_weekly','7d'),
]:
    w = raw.get(key)
    if not w or w.get('utilization') is None: continue
    used = w['utilization']; left = round(100-used, 1)
    q = {'name':label,'period':period,'used_pct':used,'left_pct':left}
    if w.get('resets_at'): q['resets_at'] = w['resets_at']
    quotas.append(q)
r = {'provider':'claude','ok':True,'checked_at':now,'quotas':quotas}
ex = raw.get('extra_usage',{})
if ex.get('is_enabled'):
    r['extra_usage'] = {'enabled':True,'used_usd':ex.get('used_credits'),'limit_usd':ex.get('monthly_limit')}
print(json.dumps(r))
" "$now" 2>/dev/null || fail_json claude "parse failed"
}

# ════════════════════════════════════════════════════════════
#  CODEX (OpenAI)
# ════════════════════════════════════════════════════════════
check_codex_json() {
  $CURL_OK || { fail_json codex "curl not available"; return; }
  local auth_file="${CODEX_HOME:-$HOME/.codex}/auth.json"
  [ ! -f "$auth_file" ] && { fail_json codex "auth file not found"; return; }

  local token refresh_token account_id
  token=$(jq -r '.tokens.access_token // empty' "$auth_file")
  refresh_token=$(jq -r '.tokens.refresh_token // empty' "$auth_file")
  [ -z "$token" ] && [ -z "$refresh_token" ] && { fail_json codex "no token found"; return; }

  account_id=$(decode_jwt_payload "$token" | jq -r '.["https://api.openai.com/auth"].chatgpt_account_id // empty' 2>/dev/null || true)

  local http_code
  http_code=$("$CURL_BIN" -s -o /tmp/_cu_codex -w '%{http_code}' "https://chatgpt.com/backend-api/wham/usage" \
    -H "Authorization: Bearer $token" \
    ${account_id:+-H "ChatGPT-Account-Id: $account_id"} 2>/dev/null || echo "000")

  if [ "$http_code" = "401" ] && [ -n "$refresh_token" ]; then
    local resp new_token
    resp=$("$CURL_BIN" -s -X POST "https://auth.openai.com/oauth/token" \
      -H "Content-Type: application/x-www-form-urlencoded" \
      -d "grant_type=refresh_token&client_id=app_EMoamEEZ73f0CkXaXp7hrann&refresh_token=$refresh_token" 2>/dev/null)
    new_token=$(echo "$resp" | jq -r '.access_token // empty')
    if [ -n "$new_token" ]; then
      local nr; nr=$(echo "$resp" | jq -r '.refresh_token // empty')
      jq --arg at "$new_token" --arg rt "${nr:-$refresh_token}" \
        '.tokens.access_token=$at | .tokens.refresh_token=$rt | .last_refresh=(now|todate)' \
        "$auth_file" > /tmp/_cu_codex_auth && mv /tmp/_cu_codex_auth "$auth_file"
      token="$new_token"
      account_id=$(decode_jwt_payload "$token" | jq -r '.["https://api.openai.com/auth"].chatgpt_account_id // empty' 2>/dev/null || true)
      http_code=$("$CURL_BIN" -s -o /tmp/_cu_codex -w '%{http_code}' "https://chatgpt.com/backend-api/wham/usage" \
        -H "Authorization: Bearer $token" \
        ${account_id:+-H "ChatGPT-Account-Id: $account_id"} 2>/dev/null || echo "000")
    fi
  fi

  [ "$http_code" != "200" ] && { rm -f /tmp/_cu_codex; fail_json codex "api returned HTTP $http_code"; return; }

  local now; now=$(ts_now)
  python3 -c "
import json, sys
from datetime import datetime, timezone
raw = json.load(sys.stdin)
now = sys.argv[1]
quotas = []
rl = raw.get('rate_limit',{})
for key, label, period in [('primary_window','session','5h'),('secondary_window','weekly','7d')]:
    w = rl.get(key)
    if not w or w.get('used_percent') is None: continue
    used = w['used_percent']; left = round(100-used,1)
    ra = w.get('reset_at','')
    if isinstance(ra,(int,float)) and ra>0: ra = datetime.fromtimestamp(ra,tz=timezone.utc).isoformat()
    q = {'name':label,'period':period,'used_pct':used,'left_pct':left}
    if ra: q['resets_at'] = str(ra)
    quotas.append(q)
for entry in (raw.get('additional_rate_limits') or []):
    if not entry: continue
    n = entry.get('limit_name','model')
    erl = entry.get('rate_limit',{})
    for wk,sx,p in [('primary_window','','5h'),('secondary_window','_weekly','7d')]:
        w = erl.get(wk)
        if not w or w.get('used_percent') is None: continue
        used = w['used_percent']; ra = w.get('reset_at','')
        if isinstance(ra,(int,float)) and ra>0: ra = datetime.fromtimestamp(ra,tz=timezone.utc).isoformat()
        q = {'name':n+sx,'period':p,'used_pct':used,'left_pct':round(100-used,1)}
        if ra: q['resets_at'] = str(ra)
        quotas.append(q)
cr = raw.get('credits',{})
r = {'provider':'codex','ok':True,'checked_at':now,'plan':raw.get('plan_type',''),'quotas':quotas,
     'credits':{'balance':cr.get('balance','0'),'has_credits':cr.get('has_credits',False)}}
print(json.dumps(r))
" "$now" < /tmp/_cu_codex 2>/dev/null || fail_json codex "parse failed"
  rm -f /tmp/_cu_codex
}

# ════════════════════════════════════════════════════════════
#  CURSOR
# ════════════════════════════════════════════════════════════
check_cursor_json() {
  local db="$HOME/Library/Application Support/Cursor/User/globalStorage/state.vscdb"
  [ ! -f "$db" ] && { fail_json cursor "not installed"; return; }

  local token refresh_token
  token=$(sqlite3 "$db" "SELECT value FROM ItemTable WHERE key='cursorAuth/accessToken' LIMIT 1" 2>/dev/null || true)
  refresh_token=$(sqlite3 "$db" "SELECT value FROM ItemTable WHERE key='cursorAuth/refreshToken' LIMIT 1" 2>/dev/null || true)
  [ -z "$token" ] && [ -z "$refresh_token" ] && { fail_json cursor "not logged in"; return; }

  # refresh if expired
  local needs_refresh=false
  if [ -n "$token" ]; then
    local exp
    exp=$(decode_jwt_payload "$token" | jq -r '.exp // 0' 2>/dev/null || echo 0)
    local now_s; now_s=$(date +%s)
    (( exp > 0 && exp < now_s + 300 )) && needs_refresh=true
  else
    needs_refresh=true
  fi

  if $needs_refresh && [ -n "$refresh_token" ]; then
    local resp new_token
    resp=$(curl -s -X POST "https://api2.cursor.sh/oauth/token" \
      -H "Content-Type: application/json" \
      -d "{\"grant_type\":\"refresh_token\",\"client_id\":\"KbZUR41cY7W6zRSdpSUJ7I7mLYBKOCmB\",\"refresh_token\":\"$refresh_token\"}" 2>/dev/null)
    new_token=$(echo "$resp" | jq -r '.access_token // empty')
    [ -n "$new_token" ] && token="$new_token"
  fi
  [ -z "$token" ] && { fail_json cursor "no valid token"; return; }

  # fetch usage + plan + credits in parallel
  local usage_body plan_body credits_body
  curl -s -X POST "https://api2.cursor.sh/aiserver.v1.DashboardService/GetCurrentPeriodUsage" \
    -H "Authorization: Bearer $token" -H "Content-Type: application/json" -H "Connect-Protocol-Version: 1" \
    -d '{}' -o /tmp/_cu_cursor_usage 2>/dev/null &
  curl -s -X POST "https://api2.cursor.sh/aiserver.v1.DashboardService/GetPlanInfo" \
    -H "Authorization: Bearer $token" -H "Content-Type: application/json" -H "Connect-Protocol-Version: 1" \
    -d '{}' -o /tmp/_cu_cursor_plan 2>/dev/null &
  curl -s -X POST "https://api2.cursor.sh/aiserver.v1.DashboardService/GetCreditGrantsBalance" \
    -H "Authorization: Bearer $token" -H "Content-Type: application/json" -H "Connect-Protocol-Version: 1" \
    -d '{}' -o /tmp/_cu_cursor_credits 2>/dev/null &
  wait

  usage_body=$(cat /tmp/_cu_cursor_usage 2>/dev/null || echo '{}')
  plan_body=$(cat /tmp/_cu_cursor_plan 2>/dev/null || echo '{}')
  credits_body=$(cat /tmp/_cu_cursor_credits 2>/dev/null || echo '{}')
  rm -f /tmp/_cu_cursor_usage /tmp/_cu_cursor_plan /tmp/_cu_cursor_credits

  local now; now=$(ts_now)
  python3 << 'PYEOF' - "$now" "$usage_body" "$plan_body" "$credits_body"
import json, sys
from datetime import datetime, timezone

now = sys.argv[1]
usage = json.loads(sys.argv[2]) if sys.argv[2] else {}
plan_data = json.loads(sys.argv[3]) if sys.argv[3] else {}
credits_data = json.loads(sys.argv[4]) if sys.argv[4] else {}

plan = ''
pi = plan_data.get('planInfo',{})
if pi.get('planName'): plan = pi['planName']

pu = usage.get('planUsage',{})
quotas = []

tp = pu.get('totalPercentUsed')
if tp is not None:
    cycle_end = usage.get('billingCycleEnd')
    ra = ''
    if cycle_end:
        try:
            ms = int(cycle_end)
            ra = datetime.fromtimestamp(ms/1000, tz=timezone.utc).isoformat()
        except: pass
    q = {'name':'total','period':'monthly','used_pct':tp,'left_pct':round(100-tp,1)}
    if ra: q['resets_at'] = ra
    quotas.append(q)
elif pu.get('limit') and pu['limit'] > 0:
    spend = pu.get('totalSpend', 0)
    limit = pu['limit']
    pct = round(spend / limit * 100, 1)
    q = {'name':'total','period':'monthly','used_pct':pct,'left_pct':round(100-pct,1),
         'used_dollars':round(spend/100,2),'limit_dollars':round(limit/100,2)}
    quotas.append(q)

for key, label in [('autoPercentUsed','auto'),('apiPercentUsed','api')]:
    v = pu.get(key)
    if v is not None:
        quotas.append({'name':label,'period':'monthly','used_pct':v,'left_pct':round(100-v,1)})

cr = {}
if credits_data.get('hasCreditGrants'):
    total_c = int(credits_data.get('totalCents',0))
    used_c = int(credits_data.get('usedCents',0))
    if total_c > 0:
        cr = {'total_usd':round(total_c/100,2),'used_usd':round(used_c/100,2),'left_usd':round((total_c-used_c)/100,2)}

r = {'provider':'cursor','ok':True,'checked_at':now,'quotas':quotas}
if plan: r['plan'] = plan
if cr: r['credits'] = cr
print(json.dumps(r))
PYEOF
  [ $? -ne 0 ] && fail_json cursor "parse failed"
}

# ════════════════════════════════════════════════════════════
#  COPILOT (GitHub)
# ════════════════════════════════════════════════════════════
check_copilot_json() {
  local token
  token=$(gh auth token 2>/dev/null || true)
  [ -z "$token" ] && { fail_json copilot "not logged in, run 'gh auth login'"; return; }

  local body
  body=$(curl -sf "https://api.github.com/copilot_internal/user" \
    -H "Authorization: token $token" \
    -H "X-Github-Api-Version: 2025-04-01" \
    -H "Editor-Version: vscode/1.96.2" \
    -H "Editor-Plugin-Version: copilot-chat/0.26.7" \
    -H "User-Agent: GitHubCopilotChat/0.26.7" 2>/dev/null || true)
  [ -z "$body" ] && { fail_json copilot "api failed, token may be expired"; return; }

  local now; now=$(ts_now)
  echo "$body" | python3 -c "
import json, sys
raw = json.load(sys.stdin)
now = sys.argv[1]
quotas = []
plan = raw.get('copilot_plan','')

snaps = raw.get('quota_snapshots',{}) or {}
reset_date = raw.get('quota_reset_date','')
for key, label in [('premium_interactions','premium'),('chat','chat')]:
    s = snaps.get(key)
    if not s or s.get('percent_remaining') is None: continue
    rem = s['percent_remaining']
    used = round(100 - rem, 1)
    q = {'name':label,'period':'monthly','used_pct':used,'left_pct':round(rem,1)}
    if reset_date: q['resets_at'] = reset_date
    quotas.append(q)

lq = raw.get('limited_user_quotas')
mq = raw.get('monthly_quotas')
if lq and mq:
    lr = raw.get('limited_user_reset_date','')
    for key, label in [('chat','chat'),('completions','completions')]:
        rem = lq.get(key); total = mq.get(key)
        if rem is None or total is None or total <= 0: continue
        used_pct = round((1 - rem/total) * 100, 1)
        q = {'name':label,'period':'monthly','used_pct':used_pct,'left_pct':round(100-used_pct,1),
             'remaining':rem,'total':total}
        if lr: q['resets_at'] = lr
        quotas.append(q)

r = {'provider':'copilot','ok':True,'checked_at':now,'quotas':quotas}
if plan: r['plan'] = plan
print(json.dumps(r))
" "$now" 2>/dev/null || fail_json copilot "parse failed"
}

# ════════════════════════════════════════════════════════════
#  GEMINI (Google)
# ════════════════════════════════════════════════════════════
check_gemini_json() {
  local creds_file="$HOME/.gemini/oauth_creds.json"
  [ ! -f "$creds_file" ] && { fail_json gemini "not logged in, run 'gemini auth login'"; return; }

  local token
  token=$(jq -r '.access_token // empty' "$creds_file" 2>/dev/null)
  [ -z "$token" ] && { fail_json gemini "no access_token in creds"; return; }

  # check if token needs refresh
  local needs_refresh=false
  local expiry
  expiry=$(jq -r '.expiry_date // 0' "$creds_file" 2>/dev/null)
  local now_ms; now_ms=$(python3 -c "import time; print(int(time.time()*1000))")
  if [ -n "$expiry" ] && [ "$expiry" != "0" ] && [ "$expiry" != "null" ]; then
    local exp_ms
    exp_ms=$(python3 -c "e=float('$expiry'); print(int(e if e>1e12 else e*1000))")
    (( exp_ms < now_ms + 300000 )) && needs_refresh=true
  fi

  if $needs_refresh; then
    local refresh_token client_id client_secret oauth2_js=""
    refresh_token=$(jq -r '.refresh_token // empty' "$creds_file" 2>/dev/null)

    # search for gemini-cli oauth client creds in install dirs
    local _search_dirs=(
      "$HOME/.bun/install/global/node_modules/@google/gemini-cli-core/dist/src/code_assist"
      "$HOME/.bun/install/global/node_modules/@google/gemini-cli/node_modules/@google/gemini-cli-core/dist/src/code_assist"
      "$HOME/.npm-global/lib/node_modules/@google/gemini-cli-core/dist/src/code_assist"
      "$HOME/.npm-global/lib/node_modules/@google/gemini-cli/node_modules/@google/gemini-cli-core/dist/src/code_assist"
      "/usr/local/lib/node_modules/@google/gemini-cli-core/dist/src/code_assist"
      "/usr/local/lib/node_modules/@google/gemini-cli/node_modules/@google/gemini-cli-core/dist/src/code_assist"
      "/opt/homebrew/opt/gemini-cli/libexec/lib/node_modules/@google/gemini-cli/bundle"
      "/usr/local/opt/gemini-cli/libexec/lib/node_modules/@google/gemini-cli/bundle"
    )

    if [ -n "$refresh_token" ]; then
      # grep across all candidate dirs for OAUTH_CLIENT_ID
      local client_id="" client_secret=""
      for dir in "${_search_dirs[@]}"; do
        [ -d "$dir" ] || continue
        local match
        match=$(find "$dir" -maxdepth 1 -name '*.js' -exec grep -l 'OAUTH_CLIENT_ID' {} + 2>/dev/null | head -1)
        [ -z "$match" ] && continue
        client_id=$(grep -o 'OAUTH_CLIENT_ID\s*=\s*"[^"]*"' "$match" 2>/dev/null | head -1 | sed 's/.*"\(.*\)"/\1/')
        client_secret=$(grep -o 'OAUTH_CLIENT_SECRET\s*=\s*"[^"]*"' "$match" 2>/dev/null | head -1 | sed 's/.*"\(.*\)"/\1/')
        [ -n "$client_id" ] && [ -n "$client_secret" ] && break
      done
      if [ -n "$client_id" ] && [ -n "$client_secret" ]; then
        local resp new_token
        resp=$(curl -s -X POST "https://oauth2.googleapis.com/token" \
          -H "Content-Type: application/x-www-form-urlencoded" \
          -d "client_id=$client_id&client_secret=$client_secret&refresh_token=$refresh_token&grant_type=refresh_token" 2>/dev/null)
        new_token=$(echo "$resp" | jq -r '.access_token // empty')
        if [ -n "$new_token" ]; then
          token="$new_token"
          local exp_in; exp_in=$(echo "$resp" | jq -r '.expires_in // empty')
          if [ -n "$exp_in" ]; then
            local new_expiry; new_expiry=$(python3 -c "import time; print(int(time.time()*1000 + $exp_in*1000))")
            jq --arg at "$new_token" --argjson ed "$new_expiry" \
              '.access_token=$at | .expiry_date=$ed' "$creds_file" > /tmp/_cu_gemini && mv /tmp/_cu_gemini "$creds_file"
          fi
        fi
      fi
    fi
  fi

  # loadCodeAssist → tier + project id
  local lca_body
  lca_body=$(curl -s -X POST "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist" \
    -H "Authorization: Bearer $token" -H "Content-Type: application/json" \
    -d '{"metadata":{"ideType":"IDE_UNSPECIFIED","platform":"PLATFORM_UNSPECIFIED","pluginType":"GEMINI","duetProject":"default"}}' 2>/dev/null || true)

  local project_id
  project_id=$(echo "${lca_body:-{}}" | python3 -c "
import json, sys
def find(obj):
    if isinstance(obj, dict):
        if 'cloudaicompanionProject' in obj and obj['cloudaicompanionProject']: return obj['cloudaicompanionProject']
        for v in obj.values():
            r = find(v)
            if r: return r
    if isinstance(obj, list):
        for v in obj:
            r = find(v)
            if r: return r
    return None
try:
    r = find(json.load(sys.stdin))
    if r: print(r)
except: pass
" 2>/dev/null || true)

  # quota
  local quota_payload='{}' quota_body
  [ -n "$project_id" ] && quota_payload="{\"project\":\"$project_id\"}"
  quota_body=$(curl -s -X POST "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuota" \
    -H "Authorization: Bearer $token" -H "Content-Type: application/json" \
    -d "$quota_payload" 2>/dev/null || true)

  [ -z "$quota_body" ] && { fail_json gemini "quota api failed"; return; }

  # check for auth error
  local is_error
  is_error=$(echo "$quota_body" | jq -r '.error.code // empty' 2>/dev/null)
  [ "$is_error" = "401" ] && { fail_json gemini "token expired, run 'gemini auth login'"; return; }

  # write to temp files for safe python consumption (printf to avoid trailing newline)
  printf '%s' "${lca_body:-{}}" > /tmp/_cu_gemini_lca
  printf '%s' "${quota_body:-{}}" > /tmp/_cu_gemini_quota

  local now; now=$(ts_now)
  python3 << 'PYEOF' - "$now" /tmp/_cu_gemini_lca /tmp/_cu_gemini_quota
import json, sys, os

now = sys.argv[1]
with open(sys.argv[2]) as f: lca = json.loads(f.read().strip())
with open(sys.argv[3]) as f: quota = json.loads(f.read().strip())
for p in [sys.argv[2], sys.argv[3]]:
    try: os.remove(p)
    except: pass

def find_str(obj, keys):
    if isinstance(obj, dict):
        for k in keys:
            if k in obj and isinstance(obj[k], str) and obj[k].strip(): return obj[k].strip()
        for v in obj.values():
            r = find_str(v, keys)
            if r: return r
    if isinstance(obj, list):
        for v in obj:
            r = find_str(v, keys)
            if r: return r
    return None

tier = find_str(lca, ['tier','userTier','subscriptionTier'])
plan_map = {'standard-tier':'Paid','free-tier':'Free','legacy-tier':'Legacy'}
plan = plan_map.get(tier,'') if tier else ''

def collect(obj, out):
    if isinstance(obj, list):
        for v in obj: collect(v, out)
        return
    if not isinstance(obj, dict): return
    if 'remainingFraction' in obj and isinstance(obj['remainingFraction'], (int,float)):
        model = obj.get('modelId') or obj.get('model_id') or 'unknown'
        out.append({'model':model, 'remaining':obj['remainingFraction'],
                    'reset':obj.get('resetTime') or obj.get('reset_time') or ''})
    for v in obj.values(): collect(v, out)

buckets = []
collect(quota, buckets)

pro = [b for b in buckets if 'pro' in b['model'].lower() and 'gemini' in b['model'].lower()]
flash = [b for b in buckets if 'flash' in b['model'].lower() and 'gemini' in b['model'].lower()]

quotas = []
for label, group in [('pro', pro), ('flash', flash)]:
    if not group: continue
    best = min(group, key=lambda b: b['remaining'])
    rem = max(0, min(1, best['remaining']))
    used = round((1-rem)*100, 1); left = round(rem*100, 1)
    q = {'name':label,'period':'rolling','used_pct':used,'left_pct':left}
    if best.get('reset'): q['resets_at'] = str(best['reset'])
    quotas.append(q)

r = {'provider':'gemini','ok':True,'checked_at':now,'quotas':quotas}
if plan: r['plan'] = plan
print(json.dumps(r))
PYEOF
  [ $? -ne 0 ] && fail_json gemini "parse failed"
}

# ════════════════════════════════════════════════════════════
#  WINDSURF (Codeium)
# ════════════════════════════════════════════════════════════
check_windsurf_json() {
  local api_key="" variant_name=""
  for marker in windsurf "windsurf-next"; do
    local db
    if [ "$marker" = "windsurf" ]; then
      db="$HOME/Library/Application Support/Windsurf/User/globalStorage/state.vscdb"
    else
      db="$HOME/Library/Application Support/Windsurf - Next/User/globalStorage/state.vscdb"
    fi
    [ ! -f "$db" ] && continue
    local auth_json
    auth_json=$(sqlite3 "$db" "SELECT value FROM ItemTable WHERE key='windsurfAuthStatus' LIMIT 1" 2>/dev/null || true)
    [ -z "$auth_json" ] && continue
    local key
    key=$(echo "$auth_json" | jq -r '.apiKey // empty' 2>/dev/null)
    if [ -n "$key" ]; then
      api_key="$key"; variant_name="$marker"; break
    fi
  done
  [ -z "$api_key" ] && { fail_json windsurf "not installed or not logged in"; return; }

  local body
  body=$(curl -s -X POST \
    "https://server.self-serve.windsurf.com/exa.seat_management_pb.SeatManagementService/GetUserStatus" \
    -H "Content-Type: application/json" -H "Connect-Protocol-Version: 1" \
    -d "{\"metadata\":{\"apiKey\":\"$api_key\",\"ideName\":\"$variant_name\",\"ideVersion\":\"1.108.2\",\"extensionName\":\"$variant_name\",\"extensionVersion\":\"1.108.2\",\"locale\":\"en\"}}" 2>/dev/null || true)
  [ -z "$body" ] && { fail_json windsurf "api request failed"; return; }

  local now; now=$(ts_now)
  echo "$body" | python3 -c "
import json, sys
from datetime import datetime, timezone
raw = json.load(sys.stdin)
now = sys.argv[1]

us = raw.get('userStatus',{})
ps = us.get('planStatus',{})
pi = ps.get('planInfo',{})
plan = pi.get('planName','').strip() if pi.get('planName') else ''

def to_iso(v):
    try: return datetime.fromtimestamp(float(v), tz=timezone.utc).isoformat()
    except: return ''

quotas = []
for rem_key, reset_key, label, period in [
    ('dailyQuotaRemainingPercent','dailyQuotaResetAtUnix','daily','1d'),
    ('weeklyQuotaRemainingPercent','weeklyQuotaResetAtUnix','weekly','7d'),
]:
    rem = ps.get(rem_key)
    if rem is None: continue
    rem = float(rem); used = round(100-rem, 1)
    q = {'name':label,'period':period,'used_pct':used,'left_pct':round(rem,1)}
    ra = to_iso(ps.get(reset_key,''))
    if ra: q['resets_at'] = ra
    quotas.append(q)

r = {'provider':'windsurf','ok':True,'checked_at':now,'quotas':quotas}
if plan: r['plan'] = plan
overage = ps.get('overageBalanceMicros')
if overage is not None:
    try:
        m = float(overage)
        if m > 0: r['extra_usage_balance'] = {'balance_usd': round(m/1e6, 2)}
    except: pass
if not quotas: r = {'provider':'windsurf','ok':False,'error':'quota data unavailable'}
print(json.dumps(r))
" "$now" 2>/dev/null || fail_json windsurf "parse failed"
}

# ════════════════════════════════════════════════════════════
#  Oneline helper
# ════════════════════════════════════════════════════════════
summarize_json() {
  python3 -c "
import json, sys
data = json.loads(sys.stdin.read())
if not data.get('ok'):
    print(data['provider'] + ': ' + data.get('error', 'unavailable'))
else:
    parts = [q['name'] + ' ' + str(q['left_pct']) + '% left' for q in data.get('quotas', [])]
    print(data['provider'] + ': ' + (', '.join(parts) if parts else 'no quota data'))
"
}

# ════════════════════════════════════════════════════════════
#  Arg parsing & dispatch
# ════════════════════════════════════════════════════════════
ONELINE=false
RAW=false
PROVIDERS=()

for arg in "$@"; do
  case "$arg" in
    --oneline|-1) ONELINE=true ;;
    --raw)        RAW=true ;;
    all)          PROVIDERS=($ALL_PROVIDERS) ;;
    claude|codex|cursor|copilot|gemini|windsurf)
                  PROVIDERS+=("$arg") ;;
    --help|-h)
      echo "usage: $0 [--oneline|-1] [--raw] [claude] [codex] [cursor] [copilot] [gemini] [windsurf] [all]"
      exit 0 ;;
    *)
      echo "unknown: $arg. available: $ALL_PROVIDERS" >&2; exit 1 ;;
  esac
done

[ ${#PROVIDERS[@]} -eq 0 ] && PROVIDERS=($ALL_PROVIDERS)

# collect results
RESULT_CLAUDE="" RESULT_CODEX="" RESULT_CURSOR="" RESULT_COPILOT="" RESULT_GEMINI="" RESULT_WINDSURF=""
for p in "${PROVIDERS[@]}"; do
  case "$p" in
    claude)   RESULT_CLAUDE=$(check_claude_json) ;;
    codex)    RESULT_CODEX=$(check_codex_json) ;;
    cursor)   RESULT_CURSOR=$(check_cursor_json) ;;
    copilot)  RESULT_COPILOT=$(check_copilot_json) ;;
    gemini)   RESULT_GEMINI=$(check_gemini_json) ;;
    windsurf) RESULT_WINDSURF=$(check_windsurf_json) ;;
  esac
done

get_result() {
  case "$1" in
    claude)   echo "$RESULT_CLAUDE" ;;
    codex)    echo "$RESULT_CODEX" ;;
    cursor)   echo "$RESULT_CURSOR" ;;
    copilot)  echo "$RESULT_COPILOT" ;;
    gemini)   echo "$RESULT_GEMINI" ;;
    windsurf) echo "$RESULT_WINDSURF" ;;
  esac
}

# output
if $RAW; then
  echo '['
  first=true
  for p in "${PROVIDERS[@]}"; do
    $first || echo ','
    get_result "$p"
    first=false
  done
  echo ']'
elif $ONELINE; then
  summaries=""
  for p in "${PROVIDERS[@]}"; do
    s=$(get_result "$p" | summarize_json)
    if [ -n "$summaries" ]; then
      summaries="$summaries | $s"
    else
      summaries="$s"
    fi
  done
  echo "[usage] $summaries"
elif [ ${#PROVIDERS[@]} -eq 1 ]; then
  get_result "${PROVIDERS[0]}" | jq .
else
  jsons=""
  for p in "${PROVIDERS[@]}"; do
    jsons+="$(get_result "$p")"$'\n'
  done
  echo "$jsons" | jq -s '.'
fi
