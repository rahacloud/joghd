package alerter

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/raha-io/joghd/internal/domain"
	"resty.dev/v3"
)

const (
	mattermostColorFailure  = "#D24B4E"
	mattermostColorRecovery = "#3DB887"
	mattermostColorReminder = "#F2B622"
)

type mattermostField struct {
	Title string `json:"title"`
	Value string `json:"value"`
	Short bool   `json:"short"`
}

type mattermostAttachment struct {
	Fallback string            `json:"fallback"`
	Color    string            `json:"color"`
	Title    string            `json:"title"`
	Text     string            `json:"text,omitempty"`
	Fields   []mattermostField `json:"fields,omitempty"`
	Footer   string            `json:"footer,omitempty"`
}

type mattermostPayload struct {
	Channel     string                 `json:"channel,omitempty"`
	Username    string                 `json:"username,omitempty"`
	IconURL     string                 `json:"icon_url,omitempty"`
	Text        string                 `json:"text,omitempty"`
	Attachments []mattermostAttachment `json:"attachments"`
}

// MattermostAlerter sends alerts via a Mattermost incoming webhook.
type MattermostAlerter struct {
	client     *resty.Client
	name       string
	webhookURL string
	channel    string
	username   string
	iconURL    string
}

// NewMattermostAlerter creates a new Mattermost alerter instance. The
// channel/username/iconURL arguments override the webhook defaults and
// may be empty.
func NewMattermostAlerter(name, webhookURL, channel, username, iconURL string, timeout time.Duration) *MattermostAlerter {
	client := resty.New().SetTimeout(timeout)

	return &MattermostAlerter{
		client:     client,
		name:       name,
		webhookURL: webhookURL,
		channel:    channel,
		username:   username,
		iconURL:    iconURL,
	}
}

// Send sends an alert via Mattermost.
func (m *MattermostAlerter) Send(ctx context.Context, alert domain.Alert) error {
	payload := mattermostPayload{
		Channel:     m.channel,
		Username:    m.username,
		IconURL:     m.iconURL,
		Attachments: []mattermostAttachment{buildMattermostAttachment(alert)},
	}

	resp, err := m.client.R().
		SetContext(ctx).
		SetBody(payload).
		Post(m.webhookURL)

	if err != nil {
		return fmt.Errorf("sending mattermost webhook: %w", err)
	}

	if resp.StatusCode() != http.StatusOK {
		return fmt.Errorf("mattermost webhook error: status %d, body: %s", resp.StatusCode(), resp.String())
	}

	return nil
}

// Name returns the alerter name in the form "mattermost:<instance>".
func (m *MattermostAlerter) Name() string {
	return "mattermost:" + m.name
}

func buildMattermostAttachment(alert domain.Alert) mattermostAttachment {
	var (
		color  string
		title  string
	)

	switch alert.Type {
	case domain.AlertTypeRecovery:
		color = mattermostColorRecovery
		title = fmt.Sprintf("🟢 RECOVERED — %s", alert.Target.Name)
	case domain.AlertTypeReminder:
		color = mattermostColorReminder
		title = fmt.Sprintf("🟡 STILL DOWN — %s", alert.Target.Name)
	default:
		color = mattermostColorFailure
		title = fmt.Sprintf("🔴 FAILURE — %s", alert.Target.Name)
	}

	fields := []mattermostField{
		{Title: "URL", Value: fmt.Sprintf("`%s`", alert.Target.URL), Short: false},
		{Title: "Expected", Value: fmt.Sprintf("%d", alert.Target.ExpectedStatus), Short: true},
		{Title: "Actual", Value: fmt.Sprintf("%d", alert.Result.ActualStatus), Short: true},
		{Title: "Latency", Value: alert.Result.Latency.Round(time.Millisecond).String(), Short: true},
		{Title: "Attempts", Value: fmt.Sprintf("%d", alert.Result.Attempts), Short: true},
	}

	if alert.Target.Company != "" {
		fields = append(fields, mattermostField{Title: "Company", Value: alert.Target.Company, Short: true})
	}
	if alert.Target.Contact != "" {
		fields = append(fields, mattermostField{Title: "Contact", Value: alert.Target.Contact, Short: true})
	}

	var text string
	if alert.Result.Error != nil && (alert.Type == domain.AlertTypeFailure || alert.Type == domain.AlertTypeReminder) {
		text = fmt.Sprintf("```\n%s\n```", alert.Result.Error.Error())
	}

	return mattermostAttachment{
		Fallback: title,
		Color:    color,
		Title:    title,
		Text:     text,
		Fields:   fields,
		Footer:   alert.Timestamp.Format("2006-01-02 15:04:05 MST"),
	}
}
