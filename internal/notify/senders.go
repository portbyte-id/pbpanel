package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
)

// secretValues lists the secret field values, so every error text can be
// redacted of them.
func secretValues(secret map[string]string) []string {
	out := make([]string, 0, len(secret))
	for _, v := range secret {
		out = append(out, v)
	}
	return out
}

// sendWebhook posts the §4.3 JSON body, signed with HMAC-SHA256 when the
// channel asks for it. Custom headers from the config are sent first; the
// PBPanel headers always win.
func (d *Dispatcher) sendWebhook(ctx context.Context, ch Channel, secret map[string]string, p payload) error {
	target, _ := ch.Config["url"].(string)
	if err := ValidateOutboundURL(target); err != nil {
		return permanent("webhook url: %v", err)
	}
	body, err := json.Marshal(webhookPayload(p))
	if err != nil {
		return permanent("encode webhook body: %v", err)
	}
	headers := map[string]string{}
	if raw, ok := ch.Config["headers"].(map[string]any); ok {
		for k, v := range raw {
			if s, ok := v.(string); ok {
				headers[k] = s
			}
		}
	}
	ts := strconv.FormatInt(d.now().Unix(), 10)
	headers["X-PBPanel-Event"] = strconv.FormatInt(p.ID, 10)
	headers["X-PBPanel-Timestamp"] = ts
	if sign, _ := ch.Config["sign"].(bool); sign && secret["hmac_secret"] != "" {
		mac := hmac.New(sha256.New, []byte(secret["hmac_secret"]))
		mac.Write([]byte(ts + "."))
		mac.Write(body)
		headers["X-PBPanel-Signature"] = "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}
	return postJSON(ctx, d.client, target, body, headers, secretValues(secret)...)
}

// sendDiscord posts one embed to the channel's webhook URL. The URL carries
// the webhook token, so it is a secret and is redacted from errors.
// (assumed API, not yet checked against the Discord docs: see §4.3, step 4.)
func (d *Dispatcher) sendDiscord(ctx context.Context, ch Channel, secret map[string]string, p payload) error {
	target := secret["webhook_url"]
	if err := ValidateOutboundURL(target); err != nil {
		return permanent("discord webhook url: %v", err)
	}
	title := p.Subject
	if p.Resolved {
		title = "Resolved: " + title
	}
	embed := map[string]any{
		"title":       clampText(title, 256),
		"description": clampText(p.Body, 4096),
		"color":       discordColour(p.Severity),
		"footer":      map[string]string{"text": clampText(p.Kind+" · "+p.Host, 2048)},
	}
	if p.Link != "" {
		embed["url"] = p.Link
	}
	body, err := json.Marshal(map[string]any{
		"allowed_mentions": map[string][]string{"parse": {}},
		"embeds":           []any{embed},
	})
	if err != nil {
		return permanent("encode discord body: %v", err)
	}
	return postJSON(ctx, d.client, target, body, nil, secretValues(secret)...)
}

// sendTelegram sends one plain-text message through the bot API. No
// parse_mode is set, so the text needs no escaping.
// (assumed API, not yet checked against the Telegram docs: see §4.3, step 4.)
func (d *Dispatcher) sendTelegram(ctx context.Context, ch Channel, secret map[string]string, p payload) error {
	token := secret["bot_token"]
	chatID, _ := ch.Config["chat_id"].(string)
	if token == "" || chatID == "" {
		return permanent("telegram needs a bot token and a chat id")
	}
	text := subjectLine(p)
	if p.Body != "" {
		text += "\n" + p.Body
	}
	if p.Link != "" {
		text += "\n" + p.Link
	}
	body, err := json.Marshal(map[string]any{
		"chat_id":                  chatID,
		"text":                     clampText(text, 4096),
		"disable_web_page_preview": true,
	})
	if err != nil {
		return permanent("encode telegram body: %v", err)
	}
	target := fmt.Sprintf("%s/bot%s/sendMessage", d.telegramBase, token)
	return postJSON(ctx, d.client, target, body, nil, secretValues(secret)...)
}
