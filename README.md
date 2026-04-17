<div align="center">
  <h1>Joghd 🦉</h1>
  <img alt="GitHub Actions Test Workflow Status" src="https://img.shields.io/github/actions/workflow/status/raha-io/joghd/test.yaml?style=for-the-badge&logo=github&label=tests">
  <img alt="GitHub Actions Release Workflow Status" src="https://img.shields.io/github/actions/workflow/status/raha-io/joghd/release.yaml?style=for-the-badge&logo=github&label=release">
  <img alt="GitHub go.mod Go version" src="https://img.shields.io/github/go-mod/go-version/raha-io/joghd?style=for-the-badge&logo=go">
  <img alt="GitHub release" src="https://img.shields.io/github/v/release/raha-io/joghd?style=for-the-badge&logo=github&sort=semver">
  <img alt="GitHub license" src="https://img.shields.io/github/license/raha-io/joghd?style=for-the-badge">
  <img alt="GitHub last commit" src="https://img.shields.io/github/last-commit/raha-io/joghd?style=for-the-badge&logo=git">
  <img alt="GitHub issues" src="https://img.shields.io/github/issues/raha-io/joghd?style=for-the-badge&logo=github">
  <img alt="GitHub pull requests" src="https://img.shields.io/github/issues-pr/raha-io/joghd?style=for-the-badge&logo=github">
  <img alt="GitHub stars" src="https://img.shields.io/github/stars/raha-io/joghd?style=for-the-badge&logo=github">
  <img alt="Go Report Card" src="https://goreportcard.com/badge/github.com/raha-io/joghd?style=for-the-badge">
  <img alt="Go Reference" src="https://img.shields.io/badge/go.dev-reference-007d9c?style=for-the-badge&logo=go&logoColor=white">
  <img alt="Code size" src="https://img.shields.io/github/languages/code-size/raha-io/joghd?style=for-the-badge">
  <br>
  <img alt="GHCR latest tag" src="https://ghcr-badge.egpl.dev/raha-io/joghd/latest_tag?trim=major&label=ghcr.io&color=%232496ED">
  <img alt="GHCR image size" src="https://ghcr-badge.egpl.dev/raha-io/joghd/size?color=%232496ED&tag=latest&label=image+size">
</div>

URL health check service written in Go. Monitors endpoints, validates HTTP status codes, and sends alerts (Telegram) on failures and recoveries.

## Features

- **Two modes**: `oneshot` (check once and exit) or `continuous` (persistent monitoring)
- **Retry with backoff**: Configurable exponential backoff before alerting
- **Telegram alerts**: Notifications for failures and recoveries
- **Extensible**: `Alerter` interface for adding new notification channels
- **Flexible config**: TOML file + environment variables via koanf

## Installation

```bash
go install github.com/raha-io/joghd/cmd/joghd@latest
```

Or build from source:

```bash
git clone https://github.com/raha-io/joghd.git
cd joghd
go build ./cmd/joghd
```

## Usage

```bash
# Oneshot mode (check once, exit 0 if healthy, 1 if any failures)
./joghd -config config.toml -mode oneshot

# Continuous mode (persistent monitoring)
./joghd -config config.toml -mode continuous
```

## Configuration

Create a `config.toml` file (see `configs/config.example.toml`):

```toml
[app]
mode = "continuous"
log_level = "info"
concurrency = 10

[http]
timeout = "10s"
user_agent = "Joghd/1.0"

[retry]
max_attempts = 3
initial_wait = "1s"
max_wait = "10s"
multiplier = 2.0

[alerters.telegram]
enabled = true
# Set via environment variables:
# JOGHD_ALERTERS_TELEGRAM_BOT_TOKEN
# JOGHD_ALERTERS_TELEGRAM_CHAT_ID

[[targets]]
name = "Production API"
url = "https://api.example.com/health"
expected_status = 200
method = "GET"
interval = "30s"

[[targets]]
name = "Staging API"
url = "https://staging.example.com/health"
expected_status = 200
method = "GET"
interval = "1m"
[targets.headers]
Authorization = "Bearer token"
```

## Environment Variables

Environment variables override config file values (prefix: `JOGHD_`):

| Variable                            | Description                          |
| ----------------------------------- | ------------------------------------ |
| `JOGHD_APP_MODE`                    | Run mode (`oneshot` or `continuous`) |
| `JOGHD_HTTP_TIMEOUT`                | Default HTTP timeout                 |
| `JOGHD_ALERTERS_TELEGRAM_BOT_TOKEN` | Telegram bot token                   |
| `JOGHD_ALERTERS_TELEGRAM_CHAT_ID`   | Telegram chat ID                     |
