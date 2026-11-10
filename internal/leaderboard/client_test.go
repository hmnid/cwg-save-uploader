package leaderboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cwg2uploader/internal/saves"
)

func TestClientUploadsSnapshot(t *testing.T) {
	var captured saves.Snapshot
	var method, path, contentType string
	var decodeErr error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, contentType = r.Method, r.URL.Path, r.Header.Get("Content-Type")
		decodeErr = json.NewDecoder(r.Body).Decode(&captured)
		_, _ = w.Write([]byte(`{"player":"Player","heroes":1,"user_hash":"123","replaced":[],"kept_newer":[]}`))
	}))
	defer server.Close()
	client := Client{HTTP: server.Client(), URL: server.URL}
	snapshot := saves.Snapshot{Files: map[string]saves.File{"HERO_01_123.pld": {Text: "save", Mtime: 1700000000123}}, Fingerprint: "local-only"}

	result, err := client.Upload(context.Background(), snapshot)

	if err != nil || decodeErr != nil {
		t.Fatalf("errors: %v, %v", err, decodeErr)
	}
	if method != "POST" || path != "/api/upload" || contentType != "application/json" {
		t.Fatal("incorrect upload request")
	}
	if captured.Files["HERO_01_123.pld"] != snapshot.Files["HERO_01_123.pld"] {
		t.Fatal("payload changed")
	}
	if result.Player != "Player" || result.Heroes != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestClientClassifiesRateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	client := Client{HTTP: server.Client(), URL: server.URL}

	_, err := client.Upload(context.Background(), saves.Snapshot{})

	var uploadError *UploadError
	if !errors.As(err, &uploadError) || !uploadError.Retry || uploadError.After != 120*time.Second {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientRejectsValidationFailureWithoutLoggingBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"sensitive save contents"}`))
	}))
	defer server.Close()
	client := Client{HTTP: server.Client(), URL: server.URL}

	_, err := client.Upload(context.Background(), saves.Snapshot{})

	var uploadError *UploadError
	if !errors.As(err, &uploadError) || uploadError.Retry || uploadError.Message != "upload rejected: HTTP 400" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientRejectsUnrecognizedSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"ok":true}`)) }))
	defer server.Close()
	client := Client{HTTP: server.Client(), URL: server.URL}

	_, err := client.Upload(context.Background(), saves.Snapshot{})

	if err == nil {
		t.Fatal("unrecognized success was accepted")
	}
}

func TestClientReportsCancelledRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	client := Client{HTTP: server.Client(), URL: server.URL}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.Upload(ctx, saves.Snapshot{})

	if err == nil {
		t.Fatal("cancelled request succeeded")
	}
}

func TestRetryAfterParsesHTTPDate(t *testing.T) {
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	value := now.Add(time.Minute).Format(http.TimeFormat)

	delay := retryAfter(value, now)

	if delay != time.Minute {
		t.Fatalf("delay = %s", delay)
	}
}
