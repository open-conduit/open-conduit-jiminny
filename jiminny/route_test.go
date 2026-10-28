package jiminny_test

import (
	"context"
	"testing"

	"github.com/open-conduit/open-conduit-jiminny/jiminny"
	"github.com/open-conduit/open-conduit-jiminny/jiminny/jiminnytest"
	"github.com/open-conduit/open-conduit/domain"
	"github.com/open-conduit/open-conduit/extension"
)

func TestRouteCreatesOnlyOnFirstSight(t *testing.T) {
	o, err := jiminny.Observe(source, jiminnytest.Call("a1", "Weekly", start), start)
	if err != nil {
		t.Fatal(err)
	}
	scope := domain.Scope{ID: "scope"}
	owner := &domain.Work{ID: jiminny.WorkID("scope", "a1"), State: domain.WorkCompleted}
	absent := domain.Event{Type: "core.ingress.absent", Source: "core.ingress", CorrelationKey: o.Event.CorrelationKey}
	unkeyed := o.Event
	unkeyed.CorrelationKey = nil
	other := o.Event
	other.Type = "jiminny.other"
	for _, c := range []struct {
		name  string
		in    extension.RouteInput
		match bool
	}{
		{"first sight", extension.RouteInput{Event: domain.AcceptedEvent{Event: o.Event}, Scope: scope}, true},
		{"owned (any state)", extension.RouteInput{Event: domain.AcceptedEvent{Event: o.Event}, Scope: scope, Work: owner}, false},
		{"absence", extension.RouteInput{Event: domain.AcceptedEvent{Event: absent}, Scope: scope, Work: owner}, false},
		{"absence, unowned", extension.RouteInput{Event: domain.AcceptedEvent{Event: absent}, Scope: scope}, false},
		{"no key", extension.RouteInput{Event: domain.AcceptedEvent{Event: unkeyed}, Scope: scope}, false},
		{"other family", extension.RouteInput{Event: domain.AcceptedEvent{Event: other}, Scope: scope}, false},
	} {
		d, err := jiminny.Route{}.Match(context.Background(), c.in)
		if err != nil || d.Match != c.match {
			t.Fatalf("%s: %+v %v", c.name, d, err)
		}
		if d.Match && (d.CreateID != "jiminny/scope/a1" || d.Kind != jiminny.Kind || len(d.AttachKeys) != 0) {
			t.Fatalf("%s: %+v", c.name, d)
		}
	}
}

func TestConfig(t *testing.T) {
	c, err := jiminny.ParseConfig(nil)
	if err != nil || c.Region != "us" || c.LookbackDays != 14 || c.IntervalMinutes != 15 || c.PageSize != 500 || c.URL() != jiminny.Regions["us"] {
		t.Fatalf("defaults %+v %v", c, err)
	}
	for _, bad := range []string{
		`{"region":"apac"}`, `{"lookback_days":181}`, `{"lookback_days":-1}`, `{"interval_minutes":1441}`,
		`{"page_size":501}`, `{"base_url":"ftp://x"}`, `{"activity_types":[""]}`, `{"token":"x"}`,
	} {
		if _, err := jiminny.ParseConfig([]byte(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	if c, err := jiminny.ParseConfig([]byte(`{"region":"eu","lookback_days":30}`)); err != nil || c.URL() != jiminny.Regions["eu"] || c.Lookback().Hours() != 720 {
		t.Fatalf("eu %+v %v", c, err)
	}
}
