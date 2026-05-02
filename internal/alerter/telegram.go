package alerter

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/rahacloud/joghd/internal/domain"
	"resty.dev/v3"
)

const sadOwlArt = "<pre>" +
	" ,___,\n" +
	" (x,x)\n" +
	" /)_)\n" +
	"  \"\"\"" +
	"</pre>"

const happyOwlArt = "<pre>" +
	" ,___,\n" +
	" (^,^)\n" +
	" /)_)\n" +
	"  \"\"\"" +
	"</pre>"

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
	client *resty.Client
	name   string
	chatID string
}

// NewTelegramAlerter creates a new Telegram alerter instance. The name
// is an operator-chosen label used in logs and error messages.
func NewTelegramAlerter(name, botToken, chatID string, timeout time.Duration) *TelegramAlerter {
	client := resty.New().
		SetBaseURL(fmt.Sprintf("https://api.telegram.org/bot%s", botToken)).
		SetTimeout(timeout)

	return &TelegramAlerter{
		client: client,
		name:   name,
		chatID: chatID,
	}
}

// Send sends an alert via Telegram.
func (t *TelegramAlerter) Send(ctx context.Context, alert domain.Alert) error {
	var result telegramResponse

	resp, err := t.client.R().
		SetContext(ctx).
		SetBody(sendMessageRequest{
			ChatID:    t.chatID,
			Text:      formatTelegramMessage(alert),
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

// Name returns the alerter name in the form "telegram:<instance>".
func (t *TelegramAlerter) Name() string {
	return "telegram:" + t.name
}

func formatTelegramMessage(alert domain.Alert) string {
	company := alert.Target.Company
	contact := alert.Target.Contact

	var header string
	var owlArt string

	switch alert.Type {
	case domain.AlertTypeRecovery:
		header = fmt.Sprintf(""+
			"🟢 <b>RECOVERED</b>\n"+
			"<b>%s</b>",
			alert.Target.Name,
		)
		owlArt = happyOwlArt
	case domain.AlertTypeReminder:
		header = fmt.Sprintf(""+
			"🟡 <b>STILL DOWN</b>\n"+
			"<b>%s</b>",
			alert.Target.Name,
		)
		owlArt = sadOwlArt
	default:
		header = fmt.Sprintf(""+
			"🔴 <b>FAILURE</b>\n"+
			"<b>%s</b>",
			alert.Target.Name,
		)
		owlArt = sadOwlArt
	}

	details := fmt.Sprintf(""+
		"🔗 <code>%s</code>\n\n"+
		"📊 <b>Expected:</b> %d → <b>Actual:</b> %d\n"+
		"⏱ <b>Latency:</b> %s\n"+
		"🔄 <b>Attempts:</b> %d\n\n"+
		"🕐 <i>%s</i>",
		alert.Target.URL,
		alert.Target.ExpectedStatus,
		alert.Result.ActualStatus,
		alert.Result.Latency.Round(time.Millisecond),
		alert.Result.Attempts,
		alert.Timestamp.Format("2006-01-02 15:04:05 MST"),
	)

	msg := header + "\n" + owlArt + "\n<blockquote>" + details + "</blockquote>"

	if alert.Result.Error != nil && (alert.Type == domain.AlertTypeFailure || alert.Type == domain.AlertTypeReminder) {
		msg += fmt.Sprintf("\n\n⚠️ <tg-spoiler>%s</tg-spoiler>", alert.Result.Error.Error())
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
