# Notes

## Product direction

This repository is not trying to replace any existing desktop tracker.

It is a focused CLI layer on top of a local usage API, optimized for:

- shell usage
- Hermes / agent usage
- scripting
- later cross-platform fallback probes

## Why Go

Chosen by default because it gives us:

- tiny install footprint
- single static-ish binaries
- easy macOS-first shipping
- straightforward cross-compilation for Windows and Linux
- no runtime dependency like Node or Python

## Source inspirations

- local HTTP usage API
- `check-usage.sh` for human-oriented presentation
- `check-usage-agent.sh` for normalized machine-oriented output

## Non-goals for v0

- duplicating every provider probe from the desktop collector
- building a TUI
- adding a local database
- adding config files before necessary
