# happyusage-cli

Tiny cross-platform CLI for checking local AI tool usage.

它分成两层：

- **human**：默认可读、带一点可视化
- **agent**：`--agent` 精简文本，`--json` 结构化输出

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

## Commands

直接输入：

```bash
hu
```

会显示帮助。

### Help

```bash
hu help
hu help usage
```

### Version

```bash
hu version
```

### Usage

默认展示所有已配置 provider 的人类可读用量视图：

```bash
hu usage
```

列出当前可读到的 providerId：

```bash
hu usage list
```

查看单个 provider：

```bash
hu usage claude
hu usage codex
```

agent 友好精简文本：

```bash
hu usage claude --agent
```

JSON 输出：

```bash
hu usage claude --json
hu usage --json
hu usage list --json
```

### Flags

```bash
--base-url   custom local API base URL
--timeout    HTTP timeout
--agent      compact agent-friendly text
--json       JSON envelope
```

## Data source

当前版本默认从本地 usage HTTP API 读取：

- `GET http://127.0.0.1:6736/v1/usage`
- `GET http://127.0.0.1:6736/v1/usage/:providerId`

## Example JSON

```json
{
  "ok": true,
  "source": "local_usage_http_api",
  "base_url": "http://127.0.0.1:6736",
  "checked_at": "2026-04-13T03:00:00Z",
  "provider": {
    "provider_id": "claude",
    "display_name": "Claude",
    "plan": "Pro",
    "progress": [
      {
        "label": "Session",
        "used": 25,
        "limit": 100,
        "remaining": 75,
        "percent_used": 25,
        "unit": "percent"
      }
    ],
    "texts": [
      {
        "label": "Today",
        "value": "$2.10 · 3M tokens"
      }
    ]
  }
}
```

## Development

```bash
go test ./...
go run ./cmd/hu
go run ./cmd/hu usage
go run ./cmd/hu usage claude --agent
go run ./cmd/hu usage claude --json
```

## Roadmap

- native provider fallback probes
- release binaries for macOS / Linux / Windows
- Homebrew first, then Scoop / winget

## License

MIT
