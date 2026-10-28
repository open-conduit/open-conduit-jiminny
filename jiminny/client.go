package jiminny

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Base URLs of Jiminny's customer API by region.
var Regions = map[string]string{
	"us": "https://app.jiminny.com/customer/api/v1",
	"eu": "https://app.jiminny.eu/customer/api/v1",
}

// Errors a page call can end with. Their text is safe to log: it never
// carries the token, a response body or customer data.
var (
	ErrUnauthorized = errors.New("jiminny: the API token was refused (401); regenerate it under Organization Settings > Integrations > API Key")
	ErrForbidden    = errors.New("jiminny: the API token may not list activities (403)")
	ErrRateLimited  = errors.New("jiminny: rate limited (429); retrying with backoff")
	ErrRequest      = errors.New("jiminny: the activity query was refused (400/422)")
	ErrUnavailable  = errors.New("jiminny: the API is unavailable")
	ErrResponse     = errors.New("jiminny: the API returned a response that could not be read")
)

// Activity is the part of a Jiminny activity this extension reads.
type Activity struct {
	ID                 string `json:"id"`
	Type               string `json:"type"`
	ActivityType       string `json:"activityType"`
	Title              string `json:"title"`
	Status             string `json:"status"`
	CreatedAt          string `json:"createdAt"`
	ActualStartTime    string `json:"actualStartTime"`
	ScheduledStartTime string `json:"scheduledStartTime"`
	UpdatedAt          string `json:"updatedAt"`
}

// ActivityPage is one /getActivities response.
type ActivityPage struct {
	Results  []Activity `json:"results"`
	Metadata struct {
		Page     float64 `json:"page"`
		PageSize int     `json:"pageSize"`
		MaxPage  int     `json:"maxPage"`
	} `json:"metadata"`
	// Failed lists activities Jiminny could not build for this page.
	Failed []string `json:"failed"`
}

// Client calls /getActivities with a team API token.
type Client struct {
	BaseURL string
	Token   func(context.Context) (string, error)
	HTTP    *http.Client
}

// QueryTime is the date-time format Jiminny's query parameters take (UTC).
const QueryTime = "2006-01-02 15:04:05"

// Activities lists processed activities that happened in [from, to].
func (c *Client) Activities(ctx context.Context, from, to time.Time, page, pageSize int) (ActivityPage, error) {
	token, err := c.Token(ctx)
	if err != nil {
		return ActivityPage{}, err
	}
	q := url.Values{}
	q.Set("fromDate", from.UTC().Format(QueryTime))
	q.Set("toDate", to.UTC().Format(QueryTime))
	q.Set("status", "completed")
	q.Set("page", strconv.Itoa(page))
	q.Set("pageSize", strconv.Itoa(pageSize))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+"/getActivities?"+q.Encode(), nil)
	if err != nil {
		return ActivityPage{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ActivityPage{}, ctx.Err()
		}
		return ActivityPage{}, ErrUnavailable
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return ActivityPage{}, ErrUnavailable
	}
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusUnauthorized:
		return ActivityPage{}, ErrUnauthorized
	case resp.StatusCode == http.StatusForbidden:
		return ActivityPage{}, ErrForbidden
	case resp.StatusCode == http.StatusTooManyRequests:
		return ActivityPage{}, ErrRateLimited
	case resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity:
		return ActivityPage{}, ErrRequest
	default:
		return ActivityPage{}, fmt.Errorf("%w (%d)", ErrUnavailable, resp.StatusCode)
	}
	var out ActivityPage
	if err := json.Unmarshal(body, &out); err != nil {
		return ActivityPage{}, ErrResponse
	}
	return out, nil
}

// parseTime reads the date-times Jiminny returns: RFC 3339, or its query
// format in UTC.
func parseTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, QueryTime} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
