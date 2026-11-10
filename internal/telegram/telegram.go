// Package telegram sends a text to one chat through the Telegram Bot API.
package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// cause drops the URL from an error of net/http or net/url: it holds the token.
func cause(err error) error {
	if u, ok := errors.AsType[*url.Error](err); ok {
		return u.Err
	}
	return err
}

// Sender returns a function that posts a text to the chat with sendMessage. The token goes
// in the URL path, as the Bot API wants it, so errors never carry the URL.
func Sender(baseURL, token, chat string) func(ctx context.Context, text string) error {
	client := &http.Client{Timeout: 10 * time.Second}
	token = strings.TrimSpace(token)
	return func(ctx context.Context, text string) error {
		form := url.Values{"chat_id": {chat}, "text": {text}}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/bot"+token+"/sendMessage", strings.NewReader(form.Encode()))
		if err != nil {
			return fmt.Errorf("telegram request: %w", cause(err))
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("telegram send: %w", cause(err))
		}
		defer func() { _ = resp.Body.Close() }() // the body is only drained
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("telegram send: http %d", resp.StatusCode)
		}
		return nil
	}
}
