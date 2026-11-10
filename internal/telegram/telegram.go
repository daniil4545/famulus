// Package telegram sends a text to one chat through the Telegram Bot API.
package telegram

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Sender returns a function that posts a text to the chat with sendMessage. The token goes
// in the URL path, as the Bot API wants it, so errors never carry the URL.
func Sender(baseURL, token, chat string) func(ctx context.Context, text string) error {
	client := &http.Client{Timeout: 10 * time.Second}
	return func(ctx context.Context, text string) error {
		form := url.Values{"chat_id": {chat}, "text": {text}}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/bot"+token+"/sendMessage", strings.NewReader(form.Encode()))
		if err != nil {
			return fmt.Errorf("telegram request: %w", err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := client.Do(req)
		if err != nil {
			// *url.Error prints the URL with the token: keep only the cause
			if u, ok := err.(*url.Error); ok { //nolint:errorlint // client.Do returns *url.Error itself
				err = u.Err
			}
			return fmt.Errorf("telegram send: %w", err)
		}
		defer func() { _ = resp.Body.Close() }() // the body is only drained
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("telegram send: http %d", resp.StatusCode)
		}
		return nil
	}
}
