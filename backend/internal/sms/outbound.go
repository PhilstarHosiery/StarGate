package sms

import (
	"context"
	"fmt"

	"github.com/android-sms-gateway/client-go/smsgateway"
)

// OutboundClient sends SMS messages via the SMS Gate API.
type OutboundClient struct {
	client *smsgateway.Client
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
	return &OutboundClient{client: smsgateway.NewClient(cfg)}
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
