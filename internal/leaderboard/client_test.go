package leaderboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientUploadsRequest(t *testing.T) {
	var captured UploadRequest
	var method, path, contentType string
	var decodeErr error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, contentType = r.Method, r.URL.Path, r.Header.Get("Content-Type")
		decodeErr = json.NewDecoder(r.Body).Decode(&captured)
		_, _ = w.Write([]byte(`{"player":"Player","heroes":1,"user_hash":"123","replaced":[],"kept_newer":[]}`))
	}))
	defer server.Close()
	client := Client{HTTP: server.Client(), URL: server.URL}
	request := UploadRequest{Files: map[string]UploadFile{"HERO_01_123.pld": {Text: "save", Mtime: 1700000000123}}}

	result, err := client.Upload(context.Background(), request)

	if err != nil || decodeErr != nil {
		t.Fatalf("errors: %v, %v", err, decodeErr)
	}
	if method != "POST" || path != "/api/upload" || contentType != "application/json" {
		t.Fatal("incorrect upload request")
	}
	if captured.Files["HERO_01_123.pld"] != request.Files["HERO_01_123.pld"] {
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

	_, err := client.Upload(context.Background(), UploadRequest{})

	var uploadError *UploadError
	if !errors.As(err, &uploadError) || !uploadError.Retry || uploadError.After != 120*time.Second {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientRetainsSmallErrorBodyForLogging(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid save"}`))
	}))
	defer server.Close()
	client := Client{HTTP: server.Client(), URL: server.URL}

	_, err := client.Upload(context.Background(), UploadRequest{})

	var uploadError *UploadError
	if !errors.As(err, &uploadError) || uploadError.Retry || uploadError.Message != `upload rejected: HTTP 400: "{\"error\":\"invalid save\"}"` || uploadError.Body != `{"error":"invalid save"}` {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientErrorBodyLoggingSizeLimit(t *testing.T) {
	cases := []struct {
		name string
		size int
		want string
	}{
		{"below limit", 4095, strings.Repeat("x", 4095)},
		{"at limit", 4096, ""},
		{"above limit", 4097, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.Repeat("x", tc.size)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusRequestTimeout)
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			client := Client{HTTP: server.Client(), URL: server.URL}

			_, err := client.Upload(context.Background(), UploadRequest{})

			var uploadError *UploadError
			if !errors.As(err, &uploadError) || !uploadError.Retry {
				t.Fatalf("unexpected error: %v", err)
			}
			if uploadError.Message != `upload rejected: HTTP 408: "`+strings.Repeat("x", 512)+`" … [truncated]` {
				t.Fatalf("missing bounded response excerpt: %s", uploadError.Message)
			}
			if uploadError.Body != tc.want {
				t.Fatalf("retained body length = %d; want %d", len(uploadError.Body), len(tc.want))
			}
		})
	}
}

func TestClientRejectsUnrecognizedSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"ok":true}`)) }))
	defer server.Close()
	client := Client{HTTP: server.Client(), URL: server.URL}

	_, err := client.Upload(context.Background(), UploadRequest{})

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

	_, err := client.Upload(ctx, UploadRequest{})

	if err == nil {
		t.Fatal("cancelled request succeeded")
	}
}

func TestResponseExcerpt(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"empty", "", "error"},
		{"controls", "timeout\nretry\x1b", `error: "timeout\nretry\x1b"`},
		{"exact limit", strings.Repeat("x", 512), `error: "` + strings.Repeat("x", 512) + `"`},
		{"over limit", strings.Repeat("x", 513), `error: "` + strings.Repeat("x", 512) + `" … [truncated]`},
		{"split rune", strings.Repeat("x", 511) + "€", `error: "` + strings.Repeat("x", 511) + `" … [truncated]`},
		{"complete rune", strings.Repeat("x", 509) + "€", `error: "` + strings.Repeat("x", 509) + `€"`},
		{"invalid UTF-8", "bad\xff", `error: "bad\xff"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			message := withResponseExcerpt("error", []byte(tc.body))

			if message != tc.want {
				t.Fatalf("message = %q; want %q", message, tc.want)
			}
		})
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
