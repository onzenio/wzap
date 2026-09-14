package webhook

// WebhookConfig is the delivery-relevant slice of an instance's webhook
// configuration: the endpoint, the enabled toggle and the subscribed event
// types. An empty URL means unset (no webhook). The 4.3 worker builds it from
// the stored instance row and consults ShouldDeliver before any Deliver call.
type WebhookConfig struct {
	URL     string
	Enabled bool
	Events  []string
}

// Subscribed reports whether eventType is in the subscribed list. Matching is
// exact and case-sensitive, mirroring ValidateEvents: "Message" never matches
// "message".
func Subscribed(events []string, eventType string) bool {
	for _, event := range events {
		if event == eventType {
			return true
		}
	}
	return false
}

// ShouldDeliver reports whether an event of eventType generates a delivery
// attempt: the webhook must be enabled, carry a URL, and subscribe the type.
// Anything else means no attempt and nothing queued — the caller must not
// reach Deliver at all.
func ShouldDeliver(cfg WebhookConfig, eventType string) bool {
	if !cfg.Enabled {
		return false
	}
	if cfg.URL == "" {
		return false
	}
	return Subscribed(cfg.Events, eventType)
}
