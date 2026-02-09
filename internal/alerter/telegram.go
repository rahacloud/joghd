package alerter

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/raha-io/joghd/internal/config"
	"github.com/raha-io/joghd/internal/domain"
	"resty.dev/v3"
)

type sendMessageRequest struct {
	ChatID    string `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode"`
}

type telegramResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
	ErrorCode   int    `json:"error_code"`
}

// TelegramAlerter sends alerts via Telegram Bot API.
type TelegramAlerter struct {
	client  *resty.Client
	chatID  string
	company string
	contact string
}

// NewTelegramAlerter creates a new Telegram alerter.
func NewTelegramAlerter(cfg config.TelegramConfig) *TelegramAlerter {
	client := resty.New().
		SetBaseURL(fmt.Sprintf("https://api.telegram.org/bot%s", cfg.BotToken)).
		SetTimeout(cfg.Timeout)

	return &TelegramAlerter{
		client:  client,
		chatID:  cfg.ChatID,
		company: cfg.Company,
		contact: cfg.Contact,
	}
}

// Send sends an alert via Telegram.
func (t *TelegramAlerter) Send(ctx context.Context, alert domain.Alert) error {
	var result telegramResponse

	resp, err := t.client.R().
		SetContext(ctx).
		SetBody(sendMessageRequest{
			ChatID:    t.chatID,
			Text:      formatTelegramMessage(alert, t.company, t.contact),
			ParseMode: "HTML",
		}).
		SetResult(&result).
		Post("/sendMessage")

	if err != nil {
		return fmt.Errorf("sending telegram message: %w", err)
	}

	if resp.StatusCode() != http.StatusOK || !result.OK {
		return fmt.Errorf("telegram API error: status %d, description: %s", result.ErrorCode, result.Description)
	}

	return nil
}

// Name returns the alerter name.
func (t *TelegramAlerter) Name() string {
	return "telegram"
}

func formatTelegramMessage(alert domain.Alert, company, contact string) string {
	var header string

	if alert.Type == domain.AlertTypeRecovery {
		header = fmt.Sprintf(""+
			"🟢 <b>RECOVERED</b>\n"+
			"<b>%s</b>",
			alert.Target.Name,
		)
	} else {
		header = fmt.Sprintf(""+
			"🔴 <b>FAILURE</b>\n"+
			"<b>%s</b>",
			alert.Target.Name,
		)
	}

	details := fmt.Sprintf(""+
		"🔗 <code>%s</code>\n\n"+
		"📊 Expected: %d → Actual: %d\n\n"+
		"⏱ Latency: %s\n\n"+
		"🔄 Attempts: %d\n\n"+
		"🕐 %s",
		alert.Target.URL,
		alert.Target.ExpectedStatus,
		alert.Result.ActualStatus,
		alert.Result.Latency.Round(time.Millisecond),
		alert.Result.Attempts,
		alert.Timestamp.Format("2006-01-02 15:04:05 MST"),
	)

	msg := header + "\n\n" + details

	if alert.Result.Error != nil && alert.Type == domain.AlertTypeFailure {
		msg += fmt.Sprintf("\n\n⚠️ <code>%s</code>", alert.Result.Error.Error())
	}

	var footer string
	if company != "" {
		footer += fmt.Sprintf("\n🏢 %s", company)
	}
	if contact != "" {
		footer += fmt.Sprintf("\n👤 %s", contact)
	}
	if footer != "" {
		msg += "\n\n<blockquote>" + footer + "\n</blockquote>"
	}

	return msg
}
