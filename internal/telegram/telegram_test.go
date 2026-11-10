package telegram

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSender(t *testing.T) {
	var path, chat, text string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, chat, text = r.URL.Path, r.FormValue("chat_id"), r.FormValue("text")
		w.WriteHeader(status)
	}))
	send := Sender(srv.URL, "123:secret", "-100")
	if err := send(t.Context(), "famulus is failing"); err != nil {
		t.Fatal(err)
	}
	if path != "/bot123:secret/sendMessage" || chat != "-100" || text != "famulus is failing" {
		t.Fatalf("request: %s chat=%s text=%q", path, chat, text)
	}

	status = http.StatusUnauthorized
	if err := send(t.Context(), "x"); err == nil || !strings.Contains(err.Error(), "http 401") {
		t.Fatalf("err = %v", err)
	}

	// an unreachable server: the error must not print the URL, it holds the token
	srv.Close()
	if err := send(t.Context(), "x"); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("err = %v", err)
	}
}
