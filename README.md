# happyusage-cli

Tiny cross-platform CLI for querying your AI tool usage from local provider usage snapshots.

It is designed for two audiences:

- **humans** — readable terminal output
- **agents** — stable JSON output with normalized fields

This repo is intentionally lightweight:

- **Go only, stdlib only**
- single binary
- works on **macOS / Linux / Windows**
- **macOS-first** because the macOS workflow is the smoothest today

## Why this exists

The underlying local collector already does the hard part well:

- reads local auth state
- refreshes tokens when needed
- talks to provider-specific private APIs
- normalizes usage into one local HTTP API

This CLI focuses on the last mile:

- a small binary that Hermes agents can call safely
- agent-friendly JSON
- human-friendly text mode
- future path to native provider probing when the local collector is unavailable

The initial version talks to a local HTTP usage API:

- `GET http://127.0.0.1:6736/v1/usage`
- `GET http://127.0.0.1:6736/v1/usage/:providerId`

## Install

### macOS / Linux

```bash
go install github.com/SunChJ/happyusage-cli/cmd/hu@latest
```

### Windows

```powershell
go install github.com/SunChJ/happyusage-cli/cmd/hu@latest
```

### Local build

```bash
go build -o bin/hu ./cmd/hu
```

## Usage

### Human-readable

```bash
hu
hu codex
hu --command health
```

### Agent-friendly JSON

```bash
hu --json
hu --json claude
hu --json --command providers
```

### Custom API address

```bash
hu --base-url http://127.0.0.1:6736 --json
```

## Commands

- `get` — default; fetch all providers or one provider
- `providers` — fetch all providers
- `health` — simple API liveness check
- `version` — print version

## Output shape

JSON mode returns a normalized envelope like:

```json
{
  "ok": true,
  "source": "local_usage_http_api",
  "base_url": "http://127.0.0.1:6736",
  "checked_at": "2026-04-13T03:00:00Z",
  "providers": [
    {
      "provider_id": "codex",
      "display_name": "Codex",
      "plan": "Plus",
      "progress": [
        {
          "label": "Session",
          "used": 1,
          "limit": 100,
          "remaining": 99,
          "percent_used": 1,
          "unit": "percent"
        }
      ],
      "texts": [],
      "badges": []
    }
  ]
}
```

## Provider roadmap

This project is informed by two shell scripts extracted from real usage work:

- `check-usage.sh` — human-friendly display
- `check-usage-agent.sh` — agent-friendly JSON

Those scripts already capture useful provider knowledge for:

- Claude
- Codex
- Cursor
- Copilot
- Gemini
- Windsurf

Planned evolution:

1. **Stage 1** — thin local usage API client ✅
2. **Stage 2** — optional native fallback probes for key providers
3. **Stage 3** — packaged releases for macOS / Linux / Windows
4. **Stage 4** — Homebrew tap first, then Scoop / winget

## Development

```bash
go test ./...
go run ./cmd/hu --json
go run ./cmd/hu codex
```

## License

MIT
