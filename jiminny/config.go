package jiminny

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Config is a jiminny entry's config. Every field is optional.
type Config struct {
	// Scope is the scope the entry's calls become Work in: the containing
	// scope (Open Conduit requires every ingress entry to name it).
	Scope string `json:"scope,omitempty"`
	// Region selects the API host: "us" (default) or "eu".
	Region string `json:"region,omitempty"`
	// BaseURL replaces the region's API host (a fake service in tests).
	BaseURL string `json:"base_url,omitempty"`
	// LookbackDays is how far back each scan looks for calls that
	// happened (default 14, at most 180: Jiminny refuses wider windows).
	// It is the recovery horizon after the daemon has been stopped.
	LookbackDays int `json:"lookback_days,omitempty"`
	// IntervalMinutes is how often a full scan starts (default 15).
	IntervalMinutes int `json:"interval_minutes,omitempty"`
	// PageSize is the activities per API page (default and at most 500).
	PageSize int `json:"page_size,omitempty"`
	// Types are the activity kinds discovered (default the calls and
	// meetings: conference, softphone, softphone-inbound).
	Types []string `json:"types,omitempty"`
	// ActivityTypes, when set, keeps only calls whose user-set activity
	// type is one of these (for example "Weekly", "Workshop").
	ActivityTypes []string `json:"activity_types,omitempty"`
	// TitleContains, when set, keeps only calls whose title contains one
	// of these, ignoring case.
	TitleContains []string `json:"title_contains,omitempty"`
}

// TokenSecret is the jiminny connection's secret holding the API token.
const TokenSecret = "token"

var errConfig = errors.New("invalid jiminny config")

func configError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errConfig, fmt.Sprintf(format, args...))
}

// ParseConfig validates an entry's config and fills its defaults.
func ParseConfig(raw []byte) (Config, error) {
	var c Config
	if trimmed := bytes.TrimSpace(raw); len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		d := json.NewDecoder(bytes.NewReader(trimmed))
		d.DisallowUnknownFields()
		if err := d.Decode(&c); err != nil {
			return Config{}, configError("malformed config or unknown field")
		}
	}
	if c.Region == "" {
		c.Region = "us"
	}
	if _, ok := Regions[c.Region]; !ok {
		return Config{}, configError("region must be us or eu")
	}
	if c.BaseURL != "" {
		u, err := url.Parse(c.BaseURL)
		if err != nil || u.Host == "" || u.Scheme != "https" && u.Scheme != "http" {
			return Config{}, configError("base_url must be an http(s) URL")
		}
	}
	if c.LookbackDays == 0 {
		c.LookbackDays = 14
	}
	if c.LookbackDays < 1 || c.LookbackDays > 180 {
		return Config{}, configError("lookback_days must be between 1 and 180")
	}
	if c.IntervalMinutes == 0 {
		c.IntervalMinutes = 15
	}
	if c.IntervalMinutes < 1 || c.IntervalMinutes > 24*60 {
		return Config{}, configError("interval_minutes must be between 1 and 1440")
	}
	if c.PageSize == 0 {
		c.PageSize = 500
	}
	if c.PageSize < 1 || c.PageSize > 500 {
		return Config{}, configError("page_size must be between 1 and 500")
	}
	if len(c.Types) == 0 {
		c.Types = []string{"conference", "softphone", "softphone-inbound"}
	}
	for _, list := range [][]string{c.Types, c.ActivityTypes, c.TitleContains} {
		for _, s := range list {
			if strings.TrimSpace(s) == "" {
				return Config{}, configError("filters must not contain empty strings")
			}
		}
	}
	return c, nil
}

// URL is the API base the entry calls.
func (c Config) URL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return Regions[c.Region]
}

// Lookback is the scan window's length.
func (c Config) Lookback() time.Duration { return time.Duration(c.LookbackDays) * 24 * time.Hour }

// Interval is the time between full scans.
func (c Config) Interval() time.Duration { return time.Duration(c.IntervalMinutes) * time.Minute }

// Keep reports whether a processed activity is discovered.
func (c Config) Keep(a Activity) bool {
	if a.Status != "completed" || !contains(c.Types, a.Type, false) {
		return false
	}
	if len(c.ActivityTypes) > 0 && !contains(c.ActivityTypes, a.ActivityType, true) {
		return false
	}
	if len(c.TitleContains) > 0 {
		title := strings.ToLower(a.Title)
		for _, s := range c.TitleContains {
			if strings.Contains(title, strings.ToLower(s)) {
				return true
			}
		}
		return false
	}
	return true
}

func contains(list []string, s string, fold bool) bool {
	for _, v := range list {
		if v == s || fold && strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}
