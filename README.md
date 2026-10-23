<div align="center">
  <h1>Joghd 🦉</h1>
  <p><strong>Self-hosted uptime monitoring and HTTP health checks with Telegram and Mattermost alerts.</strong></p>
  <img alt="GitHub Actions Test Workflow Status" src="https://img.shields.io/github/actions/workflow/status/rahacloud/joghd/test.yaml?style=for-the-badge&logo=github&label=tests">
  <img alt="GitHub Actions Release Workflow Status" src="https://img.shields.io/github/actions/workflow/status/rahacloud/joghd/release.yaml?style=for-the-badge&logo=github&label=release">
  <img alt="GitHub go.mod Go version" src="https://img.shields.io/github/go-mod/go-version/rahacloud/joghd?style=for-the-badge&logo=go">
  <img alt="GitHub release" src="https://img.shields.io/github/v/release/rahacloud/joghd?style=for-the-badge&logo=github&sort=semver">
  <img alt="GitHub license" src="https://img.shields.io/github/license/rahacloud/joghd?style=for-the-badge">
  <img alt="GitHub last commit" src="https://img.shields.io/github/last-commit/rahacloud/joghd?style=for-the-badge&logo=git">
  <img alt="GitHub issues" src="https://img.shields.io/github/issues/rahacloud/joghd?style=for-the-badge&logo=github">
  <img alt="GitHub pull requests" src="https://img.shields.io/github/issues-pr/rahacloud/joghd?style=for-the-badge&logo=github">
  <img alt="GitHub stars" src="https://img.shields.io/github/stars/rahacloud/joghd?style=for-the-badge&logo=github">
  <a href="https://pkg.go.dev/github.com/rahacloud/joghd"><img alt="Go Reference" src="https://img.shields.io/badge/go.dev-reference-007d9c?style=for-the-badge&logo=go&logoColor=white"></a>
  <img alt="Code size" src="https://img.shields.io/github/languages/code-size/rahacloud/joghd?style=for-the-badge">
  <br>
  <img alt="GHCR latest tag" src="https://ghcr-badge.egpl.dev/rahacloud/joghd/latest_tag?trim=major&label=ghcr.io&color=%232496ED">
  <img alt="GHCR image size" src="https://ghcr-badge.egpl.dev/rahacloud/joghd/size?color=%232496ED&tag=latest&label=image+size">
</div>

**Joghd** is a small, self-hosted **uptime monitor** and **website monitoring**
tool written in **Go**. It runs **HTTP/HTTPS health checks** against a list of
**endpoints**, validates the returned **status code**, retries with exponential
backoff, and sends **downtime alerts** and **recovery notifications** to
**Telegram** and **Mattermost**.

It is a single static binary with no database, no web UI, and no agent to
install — you give it a TOML file listing URLs and where to shout, and it
either checks once and exits (`oneshot`, for **cron** and **CI**) or keeps
watching (`continuous`, for a **systemd** unit, **Docker** container, or
**Kubernetes** Deployment).

> *Joghd* (جغد) is Persian for **owl** — a bird that stays awake all night
> watching things. Hence the ASCII owl in the banner.

## Table of contents

- [Why Joghd](#why-joghd)
- [Features](#features)
- [Installation](#installation)
- [Usage](#usage)
- [Configuration](#configuration)
- [Environment variables](#environment-variables)
- [Running in production](#running-in-production)
- [How Joghd compares](#how-joghd-compares)
- [FAQ](#faq)

## Why Joghd

Use Joghd when you want **endpoint monitoring** and **status code checks**
without standing up a monitoring stack:

- **Cron-friendly health checks.** `oneshot` mode exits `1` if anything is
  down, so it drops straight into a cron job, a CI pipeline, a Kubernetes
  `Job`, or a deployment smoke test.
- **Alerting where your team already is.** Telegram bot messages and
  Mattermost incoming webhooks, no PagerDuty contract required.
- **Multi-tenant routing.** One process can watch many customers' URLs and
  route each company's alerts to a different chat.
- **No moving parts.** No Postgres, no Redis, no Prometheus, no scrape
  config. One binary, one TOML file.

## Features

- **Two modes**: `oneshot` (check once and exit) or `continuous` (persistent
  monitoring daemon)
- **Retry with exponential backoff**: configurable attempts, initial wait,
  multiplier and cap, so a single blip is not paged out
- **Failure, reminder and recovery alerts**: you are told when a target goes
  down, periodically while it stays down, and when it comes back
- **Multiple alert channels**: Telegram and Mattermost, with per-company
  routing and a catch-all
- **Per-target configuration**: method, expected status code, custom headers,
  timeout and check interval
- **TCP connect checks**: `tcp://host:port` targets watch databases, SSH and anything else that is not HTTP
- **Proxy support**: route checks and alerts through an HTTP, HTTPS or SOCKS5 proxy, globally or per target and per alerter, or force a target to go `direct`
- **Certificate expiry warnings**: an HTTPS check fails while its TLS certificate is within a configurable window of expiring, before it breaks the site
- **Jittered scheduling**: targets sharing an interval do not all probe on the
  same instant
- **Prometheus metrics**: an optional `/metrics` endpoint with each target's up/down state, latency, status code and certificate expiry, plus alert delivery counters, for Grafana dashboards and Alertmanager rules
- **Correlation IDs**: every alert carries a UUIDv7 that appears in the logs
  and in the message, so one incident is traceable end to end
- **Extensible**: implement the `Alerter` interface to add a channel
- **Flexible config**: TOML file plus environment variable overrides via
  [koanf](https://github.com/knadh/koanf)
- **Ships everywhere**: static binary, `deb`/`rpm`/`apk` packages, and a
  distroless multi-arch container image on `ghcr.io`

## Installation

### go install

```bash
go install github.com/rahacloud/joghd/cmd/joghd@latest
```

### Container image

```bash
docker pull ghcr.io/rahacloud/joghd:latest
docker run --rm -v "$PWD/config.toml:/etc/joghd/config.toml:ro" ghcr.io/rahacloud/joghd:latest
```

### Packages and binaries

Prebuilt `deb`, `rpm`, `apk` packages and `linux`/`darwin`/`windows` binaries
for `amd64` and `arm64` are attached to every
[release](https://github.com/rahacloud/joghd/releases).

### From source

```bash
git clone https://github.com/rahacloud/joghd.git
cd joghd
go build ./cmd/joghd
```

Building from source requires **Go 1.27** or newer.

## Usage

```bash
# Oneshot mode (check once, exit 0 if healthy, 1 if any failures)
./joghd -config config.toml -mode oneshot

# Continuous mode (persistent monitoring)
./joghd -config config.toml -mode continuous
```

### Flags

| Flag       | Default       | Description                                     |
| ---------- | ------------- | ----------------------------------------------- |
| `-config`  | `config.toml` | Path to the configuration file                  |
| `-mode`    | from config   | `oneshot` or `continuous`, overrides `app.mode` |
| `-quiet`   | `false`       | Suppress the startup banner (useful under cron) |
| `-version` | `false`       | Print version information and exit              |

Configuration is validated at startup: an invalid mode, a non-positive
concurrency, retry or interval value, or an enabled alerter missing its
credentials all fail fast with an explanatory error rather than starting in a
broken state.

Target state is kept in memory only, so a restart re-sends a failure alert for
any target that is still down.

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
# Default proxy for every target and alerter (optional, see "Proxies").
# proxy = "socks5h://127.0.0.1:1080"
# Fail an HTTPS check two weeks before its certificate expires (optional).
cert_expiry_warning = "336h"

[retry]
max_attempts = 3
initial_wait = "1s"
max_wait = "10s"
multiplier = 2.0

# Serve Prometheus metrics on this address (optional, see "Metrics").
[metrics]
listen = "127.0.0.1:9469"

# One or more named alerter instances. The table key is the instance
# name (used in logs). `companies` is an optional allow-list — empty or
# missing means "catch-all" and receives every alert.
[alerters.rahacloud]
type = "telegram"
enabled = true
# Secrets can be overridden via environment variables:
# JOGHD_ALERTERS__RAHACLOUD__BOT_TOKEN
# JOGHD_ALERTERS__RAHACLOUD__CHAT_ID

[alerters.acme_corp]
type = "telegram"
enabled = true
companies = ["Acme Corp"]  # only receives alerts for matching targets

[alerters.team_mattermost]
type = "mattermost"
enabled = true
webhook_url = "https://mattermost.example.com/hooks/xxxxxxxxxxxxxxxxxxxxxxxxxx"
# Optional: channel, username, icon_url to override webhook defaults
# Optional: its own route, instead of inheriting http.proxy
proxy = "direct"

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

[[targets]]
name = "Primary database"
url = "tcp://db.example.com:5432"
interval = "1m"

[[targets]]
name = "Intranet portal"
url = "https://intranet.example.com/"
proxy = "http://proxy.internal:3128"
# Self-signed and renewed by hand: no expiry warning for this one.
cert_expiry_warning = "-1s"
```

### Proxies

`http.proxy` is the default route for every target and every alerter. It takes a proxy URL with an `http`, `https`, `socks5` or `socks5h` scheme, the literal `direct` for no proxy at all, or nothing, in which case HTTP traffic follows the standard `HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY` environment variables. A target or an alerter can set its own `proxy` with the same values; one that sets none inherits `http.proxy`, so a single setting covers both the checks and the alerts. `direct` also ignores the environment variables. Credentials in a proxy URL are accepted and redacted in the logs.

### TCP checks

A target whose `url` is `tcp://host:port` is a TCP connect check: it passes when a connection to `host:port` is established within the target's `timeout`, and it is retried with the same backoff as an HTTP check. `expected_status`, `method` and `headers` do not apply to it, and its alerts say "TCP connect to host:port" instead of comparing status codes. A TCP check goes direct or through a `socks5://` / `socks5h://` proxy, which then also resolves the host name. An HTTP proxy cannot carry a raw TCP connection, so Joghd refuses to start when a `tcp://` target would use one, including one inherited from `http.proxy`; give that target `proxy = "direct"` or a SOCKS5 proxy. TCP checks do not read the proxy environment variables.

### Certificate expiry warnings

`http.cert_expiry_warning` is a duration such as `"336h"` (two weeks). While an HTTPS target's certificate expires within that window, its check fails with `certificate for <host> expires in N days (YYYY-MM-DD)`, which goes out through the usual failure, reminder and recovery alerts. The default `0` turns the check off. A target can set its own `cert_expiry_warning`: `0` or leaving it out inherits the global window, and a negative value such as `"-1s"` turns the check off for that target only. The certificate is the one from the check's own verified TLS handshake, and an expiring certificate is not retried, since retrying cannot renew it.

### Metrics

Set `metrics.listen` to a `host:port`, such as `"127.0.0.1:9469"`, and Joghd serves Prometheus metrics at `/metrics` on it. It is off by default, Joghd refuses to start if the address is taken, and the endpoint has no authentication, so bind it to localhost or a private address. Metrics only mean something in `continuous` mode.

Every per-target series carries the labels `target`, `company` and `scheme` (`http`, `https` or `tcp`), and reflects the target's latest check:

| Metric | Meaning |
| --- | --- |
| `joghd_target_up` | `1` if the latest check passed, `0` if it failed |
| `joghd_target_latency_seconds` | latency of the check's final attempt |
| `joghd_target_status_code` | HTTP status, `0` for a TCP check or no response |
| `joghd_target_attempts` | attempts the check took, retries included |
| `joghd_target_last_check_timestamp_seconds` | when the latest check ran |
| `joghd_target_cert_expiry_timestamp_seconds` | when the HTTPS certificate expires; kept through a failed handshake |
| `joghd_checks_total{result}` | checks run, `result` is `success` or `failure` |
| `joghd_alerts_sent_total{alerter,kind}` | alerts delivered, `kind` is `failure`, `reminder` or `recovery` |
| `joghd_alert_failures_total{alerter,kind}` | alerts an alerter failed to deliver |
| `joghd_build_info{version,commit}` | always `1` |

The counters exist at zero from startup, so rules such as `increase(joghd_alert_failures_total[1h]) > 0` work before anything has happened. The Go runtime and process metrics (`go_*`, `process_*`) are served too. A useful pair of alert rules: `joghd_target_up == 0` for a target that is down, and `up{job="joghd"} == 0` or `absent(joghd_build_info)` for Joghd itself, since a stopped monitor sends no alerts of its own.

## Environment variables

Environment variables override config file values (prefix: `JOGHD_`):

| Variable                              | Description                                                   |
| ------------------------------------- | ------------------------------------------------------------- |
| `JOGHD_APP__MODE`                     | Run mode (`oneshot` or `continuous`)                          |
| `JOGHD_APP__LOG_LEVEL`                | Log level (`debug`, `info`, `warn`, `error`)                  |
| `JOGHD_HTTP__TIMEOUT`                 | Default HTTP timeout                                          |
| `JOGHD_HTTP__PROXY`                   | Default proxy for checks and alerts (URL or `direct`)         |
| `JOGHD_HTTP__CERT_EXPIRY_WARNING`     | Certificate expiry window, e.g. `336h` (`0` turns it off)     |
| `JOGHD_ALERTERS__<NAME>__BOT_TOKEN`   | Telegram bot token for instance `<name>` (double underscores) |
| `JOGHD_ALERTERS__<NAME>__CHAT_ID`     | Telegram chat ID for instance `<name>`                        |
| `JOGHD_ALERTERS__<NAME>__WEBHOOK_URL` | Mattermost incoming webhook URL for instance `<name>`         |

Env variables use `__` (double underscore) to separate structural levels,
matching the koanf provider — e.g. `JOGHD_ALERTERS__RAHACLOUD__BOT_TOKEN`
overrides `alerters.rahacloud.bot_token`.

## Running in production

### cron

```cron
*/5 * * * * /usr/bin/joghd -config /etc/joghd/config.toml -mode oneshot -quiet
```

A non-zero exit status means at least one target failed, so cron's own mail —
or whatever wraps the job — sees the failure too.

### systemd

The `deb`/`rpm`/`apk` packages install the binary to `/usr/bin/joghd` and an
example configuration to `/etc/joghd/config.example.toml`. Copy it to
`/etc/joghd/config.toml` and run the binary in `continuous` mode from a unit
file.

### Kubernetes

Run the container image as a `Deployment` for `continuous` mode, or as a
`CronJob` for `oneshot` mode. Mount the configuration from a `ConfigMap` at
`/etc/joghd/config.toml` — the image's default arguments already point there —
and inject bot tokens and webhook URLs from a `Secret` using the `JOGHD_`
environment variables above.

## How Joghd compares

Joghd deliberately does less than a full monitoring platform. If you want a web
dashboard, historical graphs, or a public status page, reach for something
else:

| You want                                        | Consider                                       |
| ----------------------------------------------- | ---------------------------------------------- |
| A web UI, history, and a status page            | Uptime Kuma, Gatus, Statping                   |
| Metrics scraped into Prometheus/Grafana         | Prometheus Blackbox Exporter                   |
| Dead-man's-switch monitoring for cron jobs      | healthchecks.io, Cronitor                      |
| A hosted service with SMS/phone escalation      | Pingdom, Better Stack, UptimeRobot             |
| One binary, a TOML file, and chat alerts        | **Joghd**                                      |

## FAQ

### Does Joghd need a database?

No. State lives in memory. A restart re-sends a failure alert for any target
still down, which is the trade-off for having nothing to operate.

### How do I monitor an endpoint that requires authentication?

Add a `[targets.headers]` table to the target and set whatever header the
endpoint expects, such as `Authorization = "Bearer …"`.

### Can it check for a status other than 200?

Yes — set `expected_status` per target. Any code from 100 to 599 is accepted,
so a redirect or a deliberate 401 can be the healthy answer.

### Can one instance alert multiple teams or customers?

Yes. Define several alerter instances and give each a `companies` allow-list.
A target's `company` field decides which chats hear about it; an alerter with
no allow-list receives everything.

### How often does it re-alert while a target stays down?

Once every `app.reminder_multiplier` × the target's `interval`. Set
`reminder_multiplier = 0` to alert only on the transitions into failure and
back to healthy.

### Does it support ICMP ping, TCP ports, or DNS checks?

TCP ports, yes: a `tcp://host:port` target is a TCP connect check (see [TCP checks](#tcp-checks)). ICMP ping and DNS checks are not supported.

### Can I graph it or alert on it from Prometheus?

Yes. Set `metrics.listen` and scrape `/metrics` (see [Metrics](#metrics)).

### Can it warn me before a certificate expires?

Yes. Set `http.cert_expiry_warning`, for example to `"336h"`, and an HTTPS check fails while its certificate is within two weeks of expiring (see [Certificate expiry warnings](#certificate-expiry-warnings)).

### Can it check from behind a proxy, or through one?

Yes. `http.proxy` routes every check and alert through an HTTP, HTTPS or SOCKS5 proxy, and each target and alerter can override it, including with `direct` (see [Proxies](#proxies)).

### What does the name mean?

*Joghd* (جغد) is Persian for **owl**.

## Development

```bash
just build   # build the binary
just test    # run the test suite with coverage
just lint    # run golangci-lint
just update  # update dependencies
```

The test suite runs on a fake clock via `testing/synctest`, so the retry,
timeout and scheduling tests complete in microseconds and are not timing
dependent.

## License

GPL-3.0. See [LICENSE](LICENSE).

---

<sub>**Keywords:** uptime monitoring, uptime monitor, website monitoring, HTTP health check, health check service, endpoint monitoring, URL monitoring, status code checker, downtime alerts, availability monitoring, site monitoring, self-hosted monitoring, Telegram alerts, Telegram bot notifications, Mattermost webhook alerts, Go monitoring tool, Golang uptime checker, CLI uptime monitor, cron health check, Kubernetes health check, Docker uptime monitor, synthetic monitoring, API monitoring, TCP port monitoring, SSL certificate expiry monitoring, TLS certificate expiry alerts, SOCKS5 proxy monitoring.</sub>
