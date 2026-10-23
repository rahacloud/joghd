// Package config loads, defaults and validates the joghd configuration.
//
// Values are layered: struct defaults first, then the TOML file, then
// JOGHD_-prefixed environment variables, with "__" separating structural
// levels. Everything is validated at startup so a misconfigured run fails
// immediately instead of silently monitoring nothing.
package config

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/structs"
	"github.com/knadh/koanf/v2"
	"go.uber.org/fx"

	"github.com/rahacloud/joghd/internal/domain"
	"github.com/rahacloud/joghd/internal/proxy"
)

// EnvPrefix is stripped from environment variable names before they are
// mapped onto configuration keys, e.g. JOGHD_APP__MODE -> app.mode.
const EnvPrefix = "JOGHD_"

// Run modes accepted by app.mode.
const (
	ModeOneshot    = "oneshot"
	ModeContinuous = "continuous"
)

// Defaults applied to values the operator left unset.
const (
	DefaultConcurrency        = 10
	DefaultReminderMultiplier = 6
	DefaultHTTPTimeout        = 10 * time.Second
	DefaultUserAgent          = "Joghd/1.0"
	DefaultLogLevel           = "info"

	DefaultTargetMethod   = http.MethodGet
	DefaultTargetInterval = 30 * time.Second
	DefaultExpectedStatus = http.StatusOK
	DefaultAlerterTimeout = 10 * time.Second
)

// AlertFanoutTimeout bounds how long a single alert fan-out may take. It is a
// safety net on top of the per-alerter timeouts: without it one wedged alerter
// could stall health checking indefinitely.
const AlertFanoutTimeout = 30 * time.Second

// logLevels maps app.log_level onto slog levels.
var logLevels = map[string]slog.Level{
	"debug": slog.LevelDebug,
	"info":  slog.LevelInfo,
	"warn":  slog.LevelWarn,
	"error": slog.LevelError,
}

// ParseLogLevel resolves an app.log_level value. The bool reports whether the
// value was recognised.
func ParseLogLevel(level string) (slog.Level, bool) {
	l, ok := logLevels[strings.ToLower(level)]
	return l, ok
}

// CLIParams holds command-line parameters supplied before fx starts.
type CLIParams struct {
	ConfigPath string
	Mode       string
}

// Config holds all application configuration.
type Config struct {
	App      AppConfig                `koanf:"app"`
	HTTP     HTTPConfig               `koanf:"http"`
	Retry    RetryConfig              `koanf:"retry"`
	Metrics  MetricsConfig            `koanf:"metrics"`
	Alerters map[string]AlerterConfig `koanf:"alerters"`
	Targets  []domain.Target          `koanf:"targets"`
}

// Sections exposes the loaded configuration sections as individual fx
// dependencies. Keeping fx.Out off Config itself leaves Config usable as a
// plain koanf unmarshal target and defaults source.
type Sections struct {
	fx.Out

	App      AppConfig
	HTTP     HTTPConfig
	Retry    RetryConfig
	Metrics  MetricsConfig
	Alerters map[string]AlerterConfig
	Targets  []domain.Target
}

// AlerterType identifies the concrete alerter implementation to build.
type AlerterType string

const (
	AlerterTypeTelegram   AlerterType = "telegram"
	AlerterTypeMattermost AlerterType = "mattermost"
)

// AppConfig holds application-level settings.
type AppConfig struct {
	Mode               string `koanf:"mode"`
	LogLevel           string `koanf:"log_level"`
	Concurrency        int    `koanf:"concurrency"`
	ReminderMultiplier int    `koanf:"reminder_multiplier"`
}

// HTTPConfig holds HTTP client settings.
type HTTPConfig struct {
	Timeout             time.Duration `koanf:"timeout"`
	UserAgent           string        `koanf:"user_agent"`
	SkipTLSVerification bool          `koanf:"skip_tls_verification"`

	// Proxy is the default proxy for every target and alerter that does not
	// set its own: a proxy URL (http, https, socks5, socks5h), the literal
	// "direct", or empty to follow the HTTP_PROXY/HTTPS_PROXY environment.
	Proxy string `koanf:"proxy"`

	// CertExpiryWarning fails an https check whose certificate expires within
	// this window. Zero (the default) turns the check off; a target can set
	// its own window.
	CertExpiryWarning time.Duration `koanf:"cert_expiry_warning"`
}

// RetryConfig holds retry behavior settings.
type RetryConfig struct {
	MaxAttempts int           `koanf:"max_attempts"`
	InitialWait time.Duration `koanf:"initial_wait"`
	MaxWait     time.Duration `koanf:"max_wait"`
	Multiplier  float64       `koanf:"multiplier"`
}

// MetricsConfig holds the Prometheus metrics endpoint settings.
type MetricsConfig struct {
	// Listen is the host:port that serves /metrics, e.g. "127.0.0.1:9469".
	// Empty (the default) serves nothing.
	Listen string `koanf:"listen"`
}

// AlerterConfig holds a single alerter instance configuration. The
// enclosing map key acts as the instance name (e.g. "rahacloud"). Only
// the fields relevant to the chosen Type need to be set.
type AlerterConfig struct {
	Type      AlerterType   `koanf:"type"`
	Enabled   bool          `koanf:"enabled"`
	Companies []string      `koanf:"companies"`
	Timeout   time.Duration `koanf:"timeout"`

	// Proxy routes this alerter's requests through a proxy, with the same
	// values as http.proxy. Empty inherits http.proxy.
	Proxy string `koanf:"proxy"`

	// Telegram fields.
	BotToken string `koanf:"bot_token"`
	ChatID   string `koanf:"chat_id"`

	// Mattermost fields. Channel/Username/IconURL override the webhook
	// defaults and are all optional.
	WebhookURL string `koanf:"webhook_url"`
	Channel    string `koanf:"channel"`
	Username   string `koanf:"username"`
	IconURL    string `koanf:"icon_url"`
}

// Load loads configuration from file and environment variables.
func Load(configPath string) (*Config, error) {
	k := koanf.New(".")

	// Load defaults from struct
	if err := k.Load(structs.Provider(Default(), "koanf"), nil); err != nil {
		return nil, fmt.Errorf("loading defaults: %w", err)
	}

	// Load from TOML file if path is provided
	if configPath != "" {
		if err := k.Load(file.Provider(configPath), toml.Parser()); err != nil {
			return nil, fmt.Errorf("loading config file: %w", err)
		}
	}

	// Load from environment variables (JOGHD_ prefix). The provider hands
	// TransformFunc the raw variable name, so the prefix has to be stripped
	// here or every key lands under "joghd_..." and overrides nothing.
	if err := k.Load(env.Provider(".", env.Opt{
		Prefix: EnvPrefix,
		TransformFunc: func(key, value string) (string, any) {
			key = strings.ToLower(strings.TrimPrefix(key, EnvPrefix))

			return strings.ReplaceAll(key, "__", "."), value
		},
	}), nil); err != nil {
		return nil, fmt.Errorf("loading env config: %w", err)
	}

	cfg, err := decodeSections(&loader{k: k})
	if err != nil {
		return nil, err
	}

	// Defaults are applied before validation so that validation sees the
	// values the application will actually run with.
	applyDefaults(cfg)

	if err := validate(cfg); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	return cfg, nil
}

// loader decodes individual configuration subtrees out of a koanf instance.
type loader struct {
	k *koanf.Koanf
}

// Section decodes the configuration subtree at path into a freshly allocated
// T. It is a generic method (Go 1.27): the decoded type is chosen by each
// caller and has nothing to do with the receiver, so it cannot be hoisted onto
// loader as a type parameter the way pre-1.27 Go would have forced.
func (l *loader) Section[T any](path string) (T, error) {
	var out T

	if err := l.k.Unmarshal(path, &out); err != nil {
		return out, fmt.Errorf("unmarshaling %s config: %w", path, err)
	}

	return out, nil
}

// decodeSections assembles a Config from the individual subtrees so that a
// malformed section names itself in the error instead of failing the whole
// document anonymously.
func decodeSections(l *loader) (*Config, error) {
	app, err := l.Section[AppConfig]("app")
	if err != nil {
		return nil, err
	}

	httpCfg, err := l.Section[HTTPConfig]("http")
	if err != nil {
		return nil, err
	}

	retry, err := l.Section[RetryConfig]("retry")
	if err != nil {
		return nil, err
	}

	metrics, err := l.Section[MetricsConfig]("metrics")
	if err != nil {
		return nil, err
	}

	alerters, err := l.Section[map[string]AlerterConfig]("alerters")
	if err != nil {
		return nil, err
	}

	targets, err := l.Section[[]domain.Target]("targets")
	if err != nil {
		return nil, err
	}

	return &Config{
		App:      app,
		HTTP:     httpCfg,
		Retry:    retry,
		Metrics:  metrics,
		Alerters: alerters,
		Targets:  targets,
	}, nil
}

// ProvideConfig is the fx-compatible provider that loads configuration and
// exposes each section as a separate dependency.
func ProvideConfig(params CLIParams) (Sections, error) {
	cfg, err := Load(params.ConfigPath)
	if err != nil {
		return Sections{}, err
	}

	if params.Mode != "" {
		if err := validateMode(params.Mode); err != nil {
			return Sections{}, fmt.Errorf("-mode flag: %w", err)
		}

		cfg.App.Mode = params.Mode
	}

	return Sections{
		App:      cfg.App,
		HTTP:     cfg.HTTP,
		Retry:    cfg.Retry,
		Metrics:  cfg.Metrics,
		Alerters: cfg.Alerters,
		Targets:  cfg.Targets,
	}, nil
}

func applyDefaults(cfg *Config) {
	for name, a := range cfg.Alerters {
		if a.Timeout == 0 {
			a.Timeout = DefaultAlerterTimeout
		}
		// One http.proxy covers the alerts as well as the checks; an alerter
		// only needs its own when the two must take different routes.
		if a.Proxy == "" {
			a.Proxy = cfg.HTTP.Proxy
		}
		cfg.Alerters[name] = a
	}

	for i := range cfg.Targets {
		t := &cfg.Targets[i]

		if t.Proxy == "" {
			t.Proxy = cfg.HTTP.Proxy
		}
		// Zero inherits; a negative window stays negative and means "off".
		if t.CertExpiryWarning == 0 {
			t.CertExpiryWarning = cfg.HTTP.CertExpiryWarning
		}

		if t.Method == "" {
			t.Method = DefaultTargetMethod
		}
		if t.Timeout == 0 {
			t.Timeout = cfg.HTTP.Timeout
		}
		if t.Interval == 0 {
			t.Interval = DefaultTargetInterval
		}
		if t.ExpectedStatus == 0 {
			t.ExpectedStatus = DefaultExpectedStatus
		}
	}
}

func validate(cfg *Config) error {
	if err := validateMode(cfg.App.Mode); err != nil {
		return fmt.Errorf("app.mode: %w", err)
	}

	if _, ok := ParseLogLevel(cfg.App.LogLevel); !ok {
		return fmt.Errorf("invalid app.log_level: %q (must be debug, info, warn or error)", cfg.App.LogLevel)
	}

	// A non-positive value would turn the checker's semaphore into an
	// unbuffered channel nobody ever reads from, hanging every check.
	if cfg.App.Concurrency < 1 {
		return fmt.Errorf("app.concurrency must be at least 1, got %d", cfg.App.Concurrency)
	}

	if cfg.App.ReminderMultiplier < 0 {
		return fmt.Errorf("app.reminder_multiplier must be non-negative, got %d", cfg.App.ReminderMultiplier)
	}

	if cfg.HTTP.Timeout <= 0 {
		return fmt.Errorf("http.timeout must be positive, got %s", cfg.HTTP.Timeout)
	}

	if err := proxy.Validate(cfg.HTTP.Proxy); err != nil {
		return fmt.Errorf("http.proxy: %w", err)
	}

	if cfg.HTTP.CertExpiryWarning < 0 {
		return fmt.Errorf("http.cert_expiry_warning must not be negative, got %s (use 0 to turn it off)", cfg.HTTP.CertExpiryWarning)
	}

	if err := validateRetry(cfg.Retry); err != nil {
		return err
	}

	if cfg.Metrics.Listen != "" {
		if _, _, err := net.SplitHostPort(cfg.Metrics.Listen); err != nil {
			return fmt.Errorf("metrics.listen: %w", err)
		}
	}

	if err := validateAlerters(cfg.Alerters); err != nil {
		return err
	}

	return validateTargets(cfg.Targets)
}

func validateMode(mode string) error {
	if mode != ModeOneshot && mode != ModeContinuous {
		return fmt.Errorf("invalid mode: %q (must be %q or %q)", mode, ModeOneshot, ModeContinuous)
	}

	return nil
}

func validateRetry(r RetryConfig) error {
	// Zero attempts would report every target as down without ever sending
	// a request.
	if r.MaxAttempts < 1 {
		return fmt.Errorf("retry.max_attempts must be at least 1, got %d", r.MaxAttempts)
	}
	if r.InitialWait <= 0 {
		return fmt.Errorf("retry.initial_wait must be positive, got %s", r.InitialWait)
	}
	if r.MaxWait < r.InitialWait {
		return fmt.Errorf("retry.max_wait (%s) must be greater than or equal to retry.initial_wait (%s)", r.MaxWait, r.InitialWait)
	}
	if r.Multiplier < 1 {
		return fmt.Errorf("retry.multiplier must be at least 1, got %v", r.Multiplier)
	}

	return nil
}

func validateAlerters(alerters map[string]AlerterConfig) error {
	for name, a := range alerters {
		if !a.Enabled {
			continue
		}

		switch a.Type {
		case AlerterTypeTelegram:
			if a.BotToken == "" {
				return fmt.Errorf("alerters.%s.bot_token is required when enabled", name)
			}
			if a.ChatID == "" {
				return fmt.Errorf("alerters.%s.chat_id is required when enabled", name)
			}
		case AlerterTypeMattermost:
			if a.WebhookURL == "" {
				return fmt.Errorf("alerters.%s.webhook_url is required when enabled", name)
			}
		case "":
			return fmt.Errorf("alerters.%s.type is required", name)
		default:
			return fmt.Errorf("alerters.%s.type %q is not supported", name, a.Type)
		}

		// A zero timeout means "no timeout" to the underlying HTTP client,
		// which lets a hung alert block health checks.
		if a.Timeout <= 0 {
			return fmt.Errorf("alerters.%s.timeout must be positive, got %s", name, a.Timeout)
		}

		if err := proxy.Validate(a.Proxy); err != nil {
			return fmt.Errorf("alerters.%s.proxy: %w", name, err)
		}
	}

	return nil
}

func validateTargets(targets []domain.Target) error {
	if len(targets) == 0 {
		return fmt.Errorf("no targets configured")
	}

	seen := make(map[string]int, len(targets))

	for i, t := range targets {
		if t.Name == "" {
			return fmt.Errorf("target[%d]: name is required", i)
		}
		if t.URL == "" {
			return fmt.Errorf("target[%d] (%s): url is required", i, t.Name)
		}

		if j, ok := seen[t.Name]; ok {
			return fmt.Errorf("target[%d]: duplicate name %q (already used by target[%d])", i, t.Name, j)
		}
		seen[t.Name] = i

		u, err := url.Parse(t.URL)
		if err != nil {
			return fmt.Errorf("target[%d] (%s): invalid url %q: %w", i, t.Name, t.URL, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" && u.Scheme != domain.SchemeTCP {
			return fmt.Errorf("target[%d] (%s): url %q must use the http, https or tcp scheme", i, t.Name, t.URL)
		}
		if u.Host == "" {
			return fmt.Errorf("target[%d] (%s): url %q is missing a host", i, t.Name, t.URL)
		}
		if u.Scheme == domain.SchemeTCP && u.Port() == "" {
			return fmt.Errorf("target[%d] (%s): url %q must name a port (tcp://host:port)", i, t.Name, t.URL)
		}

		// time.NewTicker panics on a non-positive interval, which would take
		// the whole process down.
		if t.Interval <= 0 {
			return fmt.Errorf("target[%d] (%s): interval must be positive, got %s", i, t.Name, t.Interval)
		}
		if t.Timeout <= 0 {
			return fmt.Errorf("target[%d] (%s): timeout must be positive, got %s", i, t.Name, t.Timeout)
		}
		// expected_status, method and headers mean nothing to a TCP connect.
		if !t.IsTCP() && (t.ExpectedStatus < 100 || t.ExpectedStatus > 599) {
			return fmt.Errorf("target[%d] (%s): expected_status %d is not a valid HTTP status code", i, t.Name, t.ExpectedStatus)
		}
		if err := proxy.Validate(t.Proxy); err != nil {
			return fmt.Errorf("target[%d] (%s): proxy: %w", i, t.Name, err)
		}
		// An HTTP proxy only forwards HTTP; a TCP connect needs SOCKS5. The
		// proxy may be inherited from http.proxy, hence the hint.
		if t.IsTCP() && t.Proxy != "" && t.Proxy != proxy.Direct && !proxy.IsSOCKS(t.Proxy) {
			return fmt.Errorf("target[%d] (%s): proxy %q is an HTTP proxy, which cannot carry a tcp:// check; "+
				"give the target a socks5:// proxy or proxy = %q", i, t.Name, proxy.Label(t.Proxy), proxy.Direct)
		}
	}

	return nil
}
