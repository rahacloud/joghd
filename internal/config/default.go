package config

import "time"

// Default returns a Config with default values.
func Default() Config {
	return Config{
		App: AppConfig{
			Mode:               "oneshot",
			LogLevel:            "info",
			Concurrency:        10,
			ReminderMultiplier: 6,
		},
		HTTP: HTTPConfig{
			Timeout:             10 * time.Second,
			UserAgent:           "Joghd/1.0",
			SkipTLSVerification: false,
		},
		Retry: RetryConfig{
			MaxAttempts: 3,
			InitialWait: 1 * time.Second,
			MaxWait:     10 * time.Second,
			Multiplier:  2.0,
		},
		Alerters: AlertersConfig{
			Telegram: TelegramConfig{
				Enabled: false,
				Timeout: 10 * time.Second,
			},
		},
	}
}
