package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/structs"
	"github.com/knadh/koanf/v2"
	"github.com/raha-io/joghd/internal/domain"
	"go.uber.org/fx"
)

// CLIParams holds command-line parameters supplied before fx starts.
type CLIParams struct {
	ConfigPath string
	Mode       string
}

// Config holds all application configuration.
type Config struct {
	fx.Out

	App      AppConfig       `koanf:"app"`
	HTTP     HTTPConfig      `koanf:"http"`
	Retry    RetryConfig     `koanf:"retry"`
	Alerters AlertersConfig  `koanf:"alerters"`
	Targets  []domain.Target `koanf:"targets"`
}

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
}

// RetryConfig holds retry behavior settings.
type RetryConfig struct {
	MaxAttempts int           `koanf:"max_attempts"`
	InitialWait time.Duration `koanf:"initial_wait"`
	MaxWait     time.Duration `koanf:"max_wait"`
	Multiplier  float64       `koanf:"multiplier"`
}

// AlertersConfig holds alerter configurations.
type AlertersConfig struct {
	Telegram TelegramConfig `koanf:"telegram"`
}

// TelegramConfig holds Telegram alerter settings.
type TelegramConfig struct {
	Enabled  bool          `koanf:"enabled"`
	BotToken string        `koanf:"bot_token"`
	ChatID   string        `koanf:"chat_id"`
	Timeout  time.Duration `koanf:"timeout"`
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

	// Load from environment variables (JOGHD_ prefix)
	if err := k.Load(env.Provider(".", env.Opt{
		Prefix: "JOGHD_",
		TransformFunc: func(key, value string) (string, any) {
			return strings.ReplaceAll(strings.ToLower(key), "__", "."), value
		},
	}), nil); err != nil {
		return nil, fmt.Errorf("loading env config: %w", err)
	}

	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, fmt.Errorf("unmarshaling config: %w", err)
	}

	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	// Apply defaults to targets
	for i := range cfg.Targets {
		if cfg.Targets[i].Method == "" {
			cfg.Targets[i].Method = "GET"
		}
		if cfg.Targets[i].Timeout == 0 {
			cfg.Targets[i].Timeout = cfg.HTTP.Timeout
		}
		if cfg.Targets[i].Interval == 0 {
			cfg.Targets[i].Interval = 30 * time.Second
		}
		if cfg.Targets[i].ExpectedStatus == 0 {
			cfg.Targets[i].ExpectedStatus = 200
		}
	}

	return &cfg, nil
}

// ProvideConfig is the fx-compatible provider that loads configuration
// and returns it by value (required by fx.Out).
func ProvideConfig(params CLIParams) (Config, error) {
	cfg, err := Load(params.ConfigPath)
	if err != nil {
		return Config{}, err
	}

	if params.Mode != "" {
		cfg.App.Mode = params.Mode
	}

	return *cfg, nil
}

func validate(cfg *Config) error {
	if cfg.App.Mode != "oneshot" && cfg.App.Mode != "continuous" {
		return fmt.Errorf("invalid app.mode: %s (must be 'oneshot' or 'continuous')", cfg.App.Mode)
	}

	if cfg.Alerters.Telegram.Enabled {
		if cfg.Alerters.Telegram.BotToken == "" {
			return fmt.Errorf("telegram.bot_token is required when telegram is enabled")
		}
		if cfg.Alerters.Telegram.ChatID == "" {
			return fmt.Errorf("telegram.chat_id is required when telegram is enabled")
		}
	}

	if cfg.App.ReminderMultiplier < 0 {
		return fmt.Errorf("app.reminder_multiplier must be non-negative, got %d", cfg.App.ReminderMultiplier)
	}

	for i, t := range cfg.Targets {
		if t.URL == "" {
			return fmt.Errorf("target[%d]: url is required", i)
		}
		if t.Name == "" {
			return fmt.Errorf("target[%d]: name is required", i)
		}
	}

	return nil
}
