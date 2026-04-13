# happyusage

Check your AI provider usage from the terminal. Built for humans and agents.

> **For AI agents**:
>
> ```bash
> # install
> curl -fsSL https://raw.githubusercontent.com/SunChJ/happyusage/main/scripts/install.sh | sh
>
> # query usage
> hu usage --agent                 # all providers, compact text
> hu usage <provider> --agent      # single provider
> hu usage list                    # discover available provider IDs
> ```

## Install

**Homebrew**

```bash
brew tap SunChJ/happyusage && brew install hu
```

**Shell script** (macOS / Linux)

```bash
curl -fsSL https://raw.githubusercontent.com/SunChJ/happyusage/main/scripts/install.sh | sh
```

If your shell exposes a broken `curl` earlier in `PATH`, happyusage now prefers a working system/Homebrew curl automatically. You can also override it explicitly:

```bash
HAPPYUSAGE_CURL=/usr/bin/curl hu usage codex --agent
```

**Go**

```bash
go install github.com/SunChJ/happyusage/cmd/hu@latest
```

**Update**

```bash
hu update
```

## Usage

```bash
hu usage                        # all providers, human-friendly
hu usage <provider>              # single provider
hu usage list                    # list available provider IDs
hu usage [provider] --agent      # compact text for AI agents (recommended for agent use)
hu usage [provider] --json       # structured JSON for web UI / integrations / debugging
```

If you are building or prompting an AI agent, prefer `--agent` by default. Use `--json` only when you explicitly need structured output for integrations, UI rendering, or debugging.

## Supported providers

Claude · Codex · Cursor · Copilot · Gemini · Windsurf

Provider data is collected via built-in native probes — no running desktop app or external API required. macOS is the primary target; Linux and Windows support is in progress.

## Output formats

**`--agent`** — one-line key=value, easy to parse:

```
claude | session_left=77.0% | session_reset_in=4h25m | weekly_left=57.0% | weekly_reset_in=1d3h
```

**`--json`** — structured JSON, designed for web UI integration:

```json
{
  "ok": true,
  "source": "native_provider_scripts",
  "checked_at": "2026-04-13T07:36:02Z",
  "provider": {
    "provider": "claude",
    "ok": true,
    "plan": "Pro",
    "quotas": [
      {
        "name": "session",
        "period": "5h",
        "used_pct": 25,
        "left_pct": 75,
        "resets_at": "2026-04-13T12:36:03+00:00"
      },
      {
        "name": "weekly",
        "period": "7d",
        "used_pct": 43,
        "left_pct": 57,
        "resets_at": "2026-04-16T18:09:23+00:00"
      }
    ]
  }
}
```

## Development

```bash
go test ./...
go run ./cmd/hu usage
go run ./cmd/hu usage claude --agent
```

## License

MIT
