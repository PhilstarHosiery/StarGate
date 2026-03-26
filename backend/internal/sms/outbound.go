package sms

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/android-sms-gateway/client-go/smsgateway"
)

// OutboundClient sends SMS messages via the SMS Gate API.
type OutboundClient struct {
	client   *smsgateway.Client
	gateURL  string
	username string
	password string
}

// NewOutboundClient creates an OutboundClient.
// If apiKey is non-empty, cloud mode (bearer token) is used.
// Otherwise local mode (basic auth with username/password) is used.
func NewOutboundClient(gateURL, username, password, apiKey string) *OutboundClient {
	var cfg smsgateway.Config
	if apiKey != "" {
		cfg = smsgateway.Config{}.WithBaseURL(gateURL).WithJWTAuth(apiKey)
	} else {
		cfg = smsgateway.Config{}.WithBaseURL(gateURL).WithBasicAuth(username, password)
	}
	return &OutboundClient{
		client:   smsgateway.NewClient(cfg),
		gateURL:  gateURL,
		username: username,
		password: password,
	}
}

// RegisterWebhook registers this server's webhook URL with the SMS Gate app so
// it receives POST notifications for inbound SMS.
func (c *OutboundClient) RegisterWebhook(webhookURL string) error {
	body, err := json.Marshal(map[string]string{
		"id":    "stargate-inbound",
		"event": "sms:received",
		"url":   webhookURL,
	})
	if err != nil {
		return fmt.Errorf("sms: register webhook: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, c.gateURL+"/webhooks", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("sms: register webhook: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(c.username, c.password)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("sms: register webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("sms: register webhook: unexpected status %s", resp.Status)
	}
	return nil
}

// Send sends an SMS to a single recipient via the configured gateway.
// sim should be 1 (Globe) or 2 (Smart).
func (c *OutboundClient) Send(to string, sim int, message string) error {
	simNum := uint8(sim)
	msg := smsgateway.Message{
		PhoneNumbers: []string{to},
		TextMessage:  &smsgateway.TextMessage{Text: message},
		SimNumber:    &simNum,
	}
	if _, err := c.client.Send(context.Background(), msg); err != nil {
		return fmt.Errorf("sms: send: %w", err)
	}
	return nil
}
