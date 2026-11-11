package leaderboard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"
)

const BaseURL = "https://cwg2-leaderboard.rskiland.workers.dev"
const responseSizeLimit = (1 << 20)
const errorBodyLogLimit = 4 << 10
const errorBodyExcerptLimit = 512

type Client struct {
	HTTP *http.Client
	URL  string
}

type UploadFile struct {
	Text  string `json:"text"`
	Mtime int64  `json:"mtime"`
}

type UploadRequest struct {
	Files map[string]UploadFile `json:"files"`
}

type Result struct {
	Player    string            `json:"player"`
	Heroes    int               `json:"heroes"`
	UserHash  json.RawMessage   `json:"user_hash"`
	Replaced  []json.RawMessage `json:"replaced"`
	KeptNewer []json.RawMessage `json:"kept_newer"`
}

type UploadError struct {
	// Body is retained for diagnostic logging only when smaller than 4 KiB.
	Body    string
	Message string
	Retry   bool
	After   time.Duration
}

func (e *UploadError) Error() string { return e.Message }

func (c *Client) Upload(ctx context.Context, request UploadRequest) (Result, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/api/upload", bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "CWG2Uploader/0.1")
	response, err := c.HTTP.Do(req)
	if err != nil {
		return Result{}, &UploadError{Message: "upload connection failed: " + err.Error(), Retry: true}
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, responseSizeLimit+1))
	if err != nil {
		return Result{}, &UploadError{Message: "read upload response: " + err.Error(), Retry: true}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var errorBody string
		if len(data) < errorBodyLogLimit {
			errorBody = string(data)
		}
		return Result{}, &UploadError{
			Body:    errorBody,
			Message: withResponseExcerpt(fmt.Sprintf("upload rejected: HTTP %d", response.StatusCode), data),
			Retry:   response.StatusCode == 408 || response.StatusCode == 429 || response.StatusCode >= 500,
			After:   retryAfter(response.Header.Get("Retry-After"), time.Now()),
		}
	}
	if len(data) > responseSizeLimit {
		return Result{}, &UploadError{Message: withResponseExcerpt("server response exceeds safety limit", data)}
	}
	var result Result
	if err := json.Unmarshal(data, &result); err != nil || result.Player == "" || result.Heroes < 1 || len(result.UserHash) == 0 || string(result.UserHash) == "null" {
		return Result{}, &UploadError{Message: withResponseExcerpt("unrecognized upload response; success not recorded", data)}
	}
	return result, nil
}

func withResponseExcerpt(message string, data []byte) string {
	if len(data) == 0 {
		return message
	}
	end := 0
	for end < len(data) && end < errorBodyExcerptLimit {
		_, size := utf8.DecodeRune(data[end:])
		if end+size > errorBodyExcerptLimit {
			break
		}
		end += size
	}
	// Quoting escapes controls and invalid UTF-8 bytes without splitting valid runes.
	message += ": " + strconv.Quote(string(data[:end]))
	if end < len(data) {
		message += " … [truncated]"
	}
	return message
}

func retryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(value, 10, 32); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil && date.After(now) {
		return date.Sub(now)
	}
	return 0
}
