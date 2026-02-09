package alerter

import (
	"context"
	"fmt"
	"time"

	"github.com/raha-io/joghd/internal/config"
	"github.com/raha-io/joghd/internal/domain"
	"resty.dev/v3"
)

const telegramAPIURL = "https://api.telegram.org"

// TelegramAlerter sends alerts via Telegram Bot API.
type TelegramAlerter struct {
	client   *resty.Client
	botToken string
	chatID   string
}

// NewTelegramAlerter creates a new Telegram alerter.
func NewTelegramAlerter(cfg config.TelegramConfig) *TelegramAlerter {
	return &TelegramAlerter{
		client:   resty.New().SetTimeout(10 * time.Second),
		botToken: cfg.BotToken,
		chatID:   cfg.ChatID,
	}
}

// Send sends an alert via Telegram.
func (t *TelegramAlerter) Send(ctx context.Context, alert domain.Alert) error {
	message := formatTelegramMessage(alert)

	url := fmt.Sprintf("%s/bot%s/sendMessage", telegramAPIURL, t.botToken)

	resp, err := t.client.R().
		SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetBody(map[string]interface{}{
			"chat_id":    t.chatID,
			"text":       message,
			"parse_mode": "Markdown",
		}).
		Post(url)

	if err != nil {
		return fmt.Errorf("sending telegram message: %w", err)
	}

	if resp.StatusCode() != 200 {
		return fmt.Errorf("telegram API error: status %d, body: %s", resp.StatusCode(), resp.String())
	}

	return nil
}

// Name returns the alerter name.
func (t *TelegramAlerter) Name() string {
	return "telegram"
}

func formatTelegramMessage(alert domain.Alert) string {
	icon := "🔴"
	if alert.Type == domain.AlertTypeRecovery {
		icon = "🟢"
	}

	status := "FAILED"
	if alert.Type == domain.AlertTypeRecovery {
		status = "RECOVERED"
	}

	msg := fmt.Sprintf(
		"%s *%s*: %s\n\n"+
			"*Target:* %s\n"+
			"*URL:* `%s`\n"+
			"*Expected:* %d\n"+
			"*Actual:* %d\n"+
			"*Latency:* %s\n"+
			"*Attempts:* %d\n"+
			"*Time:* %s",
		icon,
		status,
		alert.Target.Name,
		alert.Target.Name,
		alert.Target.URL,
		alert.Target.ExpectedStatus,
		alert.Result.ActualStatus,
		alert.Result.Latency.Round(time.Millisecond),
		alert.Result.Attempts,
		alert.Timestamp.Format("2006-01-02 15:04:05 MST"),
	)

	if alert.Result.Error != nil && alert.Type == domain.AlertTypeFailure {
		msg += fmt.Sprintf("\n*Error:* `%s`", alert.Result.Error.Error())
	}

	return msg
}
