# happyusage

Check your AI tool usage from the terminal. Built for humans and agents.

> **For AI agents**: install with `brew tap SunChJ/happyusage && brew install hu`, then run `hu usage --agent` for compact plain-text output or `hu usage --json` for structured JSON. Single provider: `hu usage claude --agent`. List available providers: `hu usage list --json`.

## Install

**Homebrew**

```bash
brew tap SunChJ/happyusage && brew install hu

# upgrade
brew update && brew upgrade hu
```

**Shell script** (macOS / Linux)

```bash
curl -fsSL https://raw.githubusercontent.com/SunChJ/happyusage/main/scripts/install.sh | sh
```

**Go**

```bash
go install github.com/SunChJ/happyusage/cmd/hu@latest
```

## Usage

```bash
hu usage              # all providers, human-friendly
hu usage claude       # single provider
hu usage list         # list available provider IDs
hu usage --agent      # compact agent-friendly text
hu usage --json       # JSON output
```

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
    "provider": "codex",
    "ok": true,
    "checked_at": "2026-04-13T07:36:04.118256+00:00",
    "plan": "plus",
    "quotas": [
      {
        "name": "session",
        "period": "5h",
        "used_pct": 0,
        "left_pct": 100,
        "resets_at": "2026-04-13T12:36:03+00:00"
      },
      {
        "name": "weekly",
        "period": "7d",
        "used_pct": 65,
        "left_pct": 35,
        "resets_at": "2026-04-16T18:09:23+00:00"
      }
    ],
    "credits": {
      "balance": "0",
      "has_credits": false
    }
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
