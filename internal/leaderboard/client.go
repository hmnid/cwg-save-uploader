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

	"cwg2uploader/internal/saves"
)

const BaseURL = "https://cwg2-leaderboard.rskiland.workers.dev"

type Client struct {
	HTTP *http.Client
	URL  string
}

type Result struct {
	Player    string            `json:"player"`
	Heroes    int               `json:"heroes"`
	UserHash  json.RawMessage   `json:"user_hash"`
	Replaced  []json.RawMessage `json:"replaced"`
	KeptNewer []json.RawMessage `json:"kept_newer"`
}

type UploadError struct {
	Message string
	Retry   bool
	After   time.Duration
}

func (e *UploadError) Error() string { return e.Message }

func (c *Client) Upload(ctx context.Context, snapshot saves.Snapshot) (Result, error) {
	body, err := json.Marshal(snapshot)
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
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return Result{}, &UploadError{Message: "read upload response: " + err.Error(), Retry: true}
	}
	if len(data) > 1<<20 {
		return Result{}, &UploadError{Message: "server response exceeds safety limit"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// Do not log arbitrary response text: it could contain save contents.
		return Result{}, &UploadError{
			Message: fmt.Sprintf("upload rejected: HTTP %d", response.StatusCode),
			Retry:   response.StatusCode == 408 || response.StatusCode == 429 || response.StatusCode >= 500,
			After:   retryAfter(response.Header.Get("Retry-After"), time.Now()),
		}
	}
	var result Result
	if err := json.Unmarshal(data, &result); err != nil || result.Player == "" || result.Heroes < 1 || len(result.UserHash) == 0 || string(result.UserHash) == "null" {
		return Result{}, &UploadError{Message: "unrecognized upload response; success not recorded"}
	}
	return result, nil
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
