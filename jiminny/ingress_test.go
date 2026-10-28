package jiminny_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/open-conduit/open-conduit-jiminny/jiminny"
	"github.com/open-conduit/open-conduit-jiminny/jiminny/jiminnytest"
	"github.com/open-conduit/open-conduit/conformance"
	"github.com/open-conduit/open-conduit/domain"
	"github.com/open-conduit/open-conduit/extension"
)

var (
	source = extension.Source{Namespace: "jiminny/scope/calls", ScopeID: "scope"}
	start  = time.Date(2026, 10, 27, 15, 0, 0, 0, time.UTC)
)

type clock struct{ t time.Time }

func (c *clock) Now() time.Time          { return c.t }
func (c *clock) Advance(d time.Duration) { c.t = c.t.Add(d) }

func newIngress(t *testing.T, fake *jiminnytest.Server, c *clock, cfg jiminny.Config) *jiminny.Ingress {
	t.Helper()
	raw, _ := json.Marshal(cfg)
	parsed, err := jiminny.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	parsed.BaseURL = fake.URL
	return &jiminny.Ingress{Config: parsed, Now: c.Now, Client: &jiminny.Client{BaseURL: fake.URL, Token: func(context.Context) (string, error) { return jiminnytest.Token, nil }}}
}

// host models what Open Conduit's ingress and router guarantee for this
// source (internal/runtime/reconcile.go, the route contract): pages stage
// until a complete one, which diffs against the last complete snapshot; an
// Event is accepted once per delivery identity, forever; absence is a
// generic event; the route decides which Event creates Work.
type host struct {
	t          *testing.T
	in         *jiminny.Ingress
	scan       extension.Scan
	staged     map[string]extension.Observation
	current    map[string]extension.Observation
	deliveries map[string]bool
	works      map[string]domain.ID
	accepted   []domain.Event
	absent     []string
}

func newHost(t *testing.T, in *jiminny.Ingress) *host {
	return &host{t: t, in: in, scan: extension.Scan{Source: source}, staged: map[string]extension.Observation{},
		current: map[string]extension.Observation{}, deliveries: map[string]bool{}, works: map[string]domain.ID{}}
}

// page makes one page call, as ReconcileDue does.
func (h *host) page() (extension.Snapshot, error) {
	h.t.Helper()
	p, err := h.in.Reconcile(context.Background(), h.scan)
	if err != nil {
		return p, err // a failed page advances nothing
	}
	if p.ScanID == "" || h.scan.ScanID != "" && p.ScanID != h.scan.ScanID || p.Complete && p.NextCursor != "" || p.NextCursor != "" && p.NextCursor == h.scan.Cursor {
		h.t.Fatalf("invalid page %+v after %+v", p, h.scan)
	}
	for _, o := range p.Observations {
		if old, ok := h.staged[o.ID]; ok && old.Version != o.Version {
			h.t.Fatalf("conflicting versions of %s in one generation", o.ID)
		}
		h.staged[o.ID] = o
	}
	switch {
	case p.Complete:
		h.publish()
		h.scan = extension.Scan{Source: source, Checkpoint: p.Checkpoint}
	case p.NextCursor == "":
		// An exhausted partial scan discards its staging only.
		h.staged = map[string]extension.Observation{}
		h.scan = extension.Scan{Source: source, Checkpoint: h.scan.Checkpoint}
	default:
		h.scan.ScanID, h.scan.Cursor = p.ScanID, p.NextCursor
	}
	return p, nil
}

func (h *host) publish() {
	for id, o := range h.staged {
		if old, ok := h.current[id]; !ok || old.Version != o.Version {
			h.accept(o.Event)
		}
	}
	for id, o := range h.current {
		if _, ok := h.staged[id]; !ok {
			h.absent = append(h.absent, id)
			h.route(domain.Event{Type: "core.ingress.absent", Source: "core.ingress", DeliveryID: "core.absent/" + h.scan.ScanID + "/" + id, CorrelationKey: o.Event.CorrelationKey})
		}
	}
	h.current, h.staged = h.staged, map[string]extension.Observation{}
}

func (h *host) accept(e domain.Event) {
	if h.deliveries[e.Source+"\x00"+e.DeliveryID] {
		return // deduplicated: the original record, nothing new to route
	}
	h.deliveries[e.Source+"\x00"+e.DeliveryID] = true
	h.accepted = append(h.accepted, e)
	h.route(e)
}

func (h *host) route(e domain.Event) {
	in := extension.RouteInput{Event: domain.AcceptedEvent{Event: e}, Scope: domain.Scope{ID: source.ScopeID}}
	if e.CorrelationKey != nil {
		if id, ok := h.works[e.CorrelationKey.ExternalID]; ok {
			in.Work = &domain.Work{ID: id}
		}
	}
	d, err := jiminny.Route{}.Match(context.Background(), in)
	if err != nil {
		h.t.Fatal(err)
	}
	if d.Match && in.Work == nil {
		h.works[e.CorrelationKey.ExternalID] = d.CreateID
	}
}

// scan runs page calls until a generation completes, failing on an error.
func (h *host) scanAll() {
	h.t.Helper()
	for n := 0; n < 100; n++ {
		p, err := h.page()
		if err != nil {
			h.t.Fatal(err)
		}
		if p.Complete {
			return
		}
	}
	h.t.Fatal("scan did not finish")
}

func (h *host) workIDs() []string {
	var ids []string
	for id := range h.works {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func TestIngressConformance(t *testing.T) {
	c := &clock{start}
	call := jiminnytest.Call("a1", "Weekly progress", start.Add(-time.Hour))
	fake := jiminnytest.New(call, jiminnytest.Call("a2", "Workshop", start.Add(-2*time.Hour)))
	defer fake.Close()
	in := newIngress(t, fake, c, jiminny.Config{PageSize: 1})
	body, _ := json.Marshal(call)
	canonical, err := jiminny.Observe(source, call, start)
	if err != nil {
		t.Fatal(err)
	}
	conformance.RunIngress(t, func() conformance.IngressFixture {
		return conformance.IngressFixture{Ingress: in, Source: source, Delivery: extension.Delivery{ID: "push", ReceivedAt: start, Body: body}, Canonical: canonical}
	})
}

func TestDiscoveryIsOncePerCall(t *testing.T) {
	c := &clock{start}
	fake := jiminnytest.New(jiminnytest.Call("a1", "Weekly progress", start.Add(-time.Hour)))
	defer fake.Close()
	h := newHost(t, newIngress(t, fake, c, jiminny.Config{}))

	h.scanAll()
	if len(h.accepted) != 1 || len(h.works) != 1 || h.works["a1"] != "jiminny/scope/a1" {
		t.Fatalf("first discovery: %d events, works %v", len(h.accepted), h.works)
	}
	var p jiminny.Payload
	if err := json.Unmarshal(h.accepted[0].Data.Data, &p); err != nil || p.ActivityID != "a1" || p.Title != "Weekly progress" || p.ActivityType != "Weekly" {
		t.Fatalf("payload %s", h.accepted[0].Data.Data)
	}

	// Rescans of the same data, and the finished call still listed, change
	// nothing.
	for i := 0; i < 3; i++ {
		c.Advance(15 * time.Minute)
		h.scanAll()
	}
	if len(h.accepted) != 1 || len(h.works) != 1 {
		t.Fatalf("rescans accepted %d events, works %v", len(h.accepted), h.works)
	}

	// A restarted process asks again with nothing but the host's scan state.
	c.Advance(15 * time.Minute)
	h.in = newIngress(t, fake, c, jiminny.Config{})
	h.scanAll()
	if len(h.accepted) != 1 {
		t.Fatalf("restart re-accepted: %d events", len(h.accepted))
	}
}

func TestOfflineForDaysRecoversWithinLookback(t *testing.T) {
	c := &clock{start}
	fake := jiminnytest.New(jiminnytest.Call("before", "Before", start.Add(-time.Hour)))
	defer fake.Close()
	h := newHost(t, newIngress(t, fake, c, jiminny.Config{LookbackDays: 7}))
	h.scanAll()

	// Stopped for four days while calls were processed.
	for d := 1; d <= 4; d++ {
		fake.Put(jiminnytest.Call(fmt.Sprintf("day%d", d), "Weekly", start.Add(time.Duration(d)*24*time.Hour)))
	}
	c.Advance(4*24*time.Hour + time.Hour)
	h.in = newIngress(t, fake, c, jiminny.Config{LookbackDays: 7})
	h.scanAll()
	if got := h.workIDs(); fmt.Sprint(got) != "[before day1 day2 day3 day4]" {
		t.Fatalf("works after offline recovery %v", got)
	}

	// A call older than the window is out of reach: documented, and it
	// makes no Work.
	fake.Put(jiminnytest.Call("ancient", "Old", start.Add(-30*24*time.Hour)))
	h.scanAll()
	if _, ok := h.works["ancient"]; ok {
		t.Fatal("a call outside the lookback made Work")
	}
}

func TestAgeingOutAndBackIsInert(t *testing.T) {
	c := &clock{start}
	fake := jiminnytest.New(jiminnytest.Call("old", "Weekly", start.Add(-6*24*time.Hour)), jiminnytest.Call("new", "Weekly", start))
	defer fake.Close()
	h := newHost(t, newIngress(t, fake, c, jiminny.Config{LookbackDays: 7}))
	h.scanAll()
	c.Advance(2 * 24 * time.Hour)
	h.scanAll()
	if fmt.Sprint(h.absent) != "[old]" || len(h.accepted) != 2 || len(h.works) != 2 {
		t.Fatalf("ageing out: absent %v, %d events, works %v", h.absent, len(h.accepted), h.works)
	}
	// A wider lookback brings it back: the same identity, so nothing new.
	h.in = newIngress(t, fake, c, jiminny.Config{LookbackDays: 30})
	h.scanAll()
	if len(h.accepted) != 2 || len(h.works) != 2 {
		t.Fatalf("reappearing call: %d events, works %v", len(h.accepted), h.works)
	}
}

func TestEditedCallKeepsItsWork(t *testing.T) {
	c := &clock{start}
	call := jiminnytest.Call("a1", "Weekly progress", start.Add(-time.Hour))
	fake := jiminnytest.New(call)
	defer fake.Close()
	h := newHost(t, newIngress(t, fake, c, jiminny.Config{}))
	h.scanAll()
	call.Title, call.UpdatedAt = "Weekly progress (renamed)", start.Format(time.RFC3339)
	fake.Put(call)
	h.scanAll()
	// The new version is accepted as history on the same key, and the route
	// sends it nowhere: no second Work, no new minutes.
	if len(h.accepted) != 2 || len(h.works) != 1 || h.accepted[0].CorrelationKey.ExternalID != h.accepted[1].CorrelationKey.ExternalID {
		t.Fatalf("edit: %d events, works %v", len(h.accepted), h.works)
	}
	// updatedAt alone is not part of the identity.
	call.UpdatedAt = start.Add(time.Hour).Format(time.RFC3339)
	fake.Put(call)
	h.scanAll()
	if len(h.accepted) != 2 {
		t.Fatalf("an updatedAt bump made a new version: %d events", len(h.accepted))
	}
}

func TestFailedPagesPublishNothing(t *testing.T) {
	c := &clock{start}
	var calls []jiminny.Activity
	for i := 0; i < 5; i++ {
		calls = append(calls, jiminnytest.Call(fmt.Sprintf("c%d", i), "Weekly", start.Add(-time.Duration(i+1)*time.Hour)))
	}
	fake := jiminnytest.New(calls...)
	defer fake.Close()
	h := newHost(t, newIngress(t, fake, c, jiminny.Config{PageSize: 2}))
	h.scanAll()

	// Every call vanishes from a scan that fails on its last page: nothing
	// is published, so nothing is absent.
	for _, a := range calls {
		fake.Delete(a.ID)
	}
	fake.Put(calls[0])
	fake.Put(calls[1])
	fake.Put(calls[2])
	if _, err := h.page(); err != nil {
		t.Fatal(err)
	}
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusUnauthorized} {
		fake.Fail(status)
		_, err := h.page()
		if err == nil {
			t.Fatalf("status %d: no error", status)
		}
		if status == http.StatusTooManyRequests && !errors.Is(err, jiminny.ErrRateLimited) || status == http.StatusUnauthorized && !errors.Is(err, jiminny.ErrUnauthorized) {
			t.Fatalf("status %d: %v", status, err)
		}
	}
	if len(h.absent) != 0 {
		t.Fatalf("a failed scan published absence %v", h.absent)
	}
	// The retry resumes the same generation at the same page.
	h.scanAll()
	sort.Strings(h.absent)
	if fmt.Sprint(h.absent) != "[c3 c4]" {
		t.Fatalf("after the retry completes: absent %v", h.absent)
	}
	if len(h.works) != 5 || len(h.accepted) != 5 {
		t.Fatalf("works %v, %d events", h.works, len(h.accepted))
	}
}

func TestRestartMidScanResumes(t *testing.T) {
	c := &clock{start}
	var calls []jiminny.Activity
	for i := 0; i < 4; i++ {
		calls = append(calls, jiminnytest.Call(fmt.Sprintf("c%d", i), "Weekly", start.Add(-time.Duration(i+1)*time.Hour)))
	}
	fake := jiminnytest.New(calls...)
	defer fake.Close()
	h := newHost(t, newIngress(t, fake, c, jiminny.Config{PageSize: 1}))
	if _, err := h.page(); err != nil {
		t.Fatal(err)
	}
	if _, err := h.page(); err != nil {
		t.Fatal(err)
	}
	// The daemon stops for a day mid-scan; the new process continues the
	// staged generation over its original window.
	c.Advance(24 * time.Hour)
	fake.Put(jiminnytest.Call("later", "Weekly", start.Add(12*time.Hour)))
	h.in = newIngress(t, fake, c, jiminny.Config{PageSize: 1})
	h.scanAll()
	if len(h.works) != 4 {
		t.Fatalf("resumed generation: works %v", h.workIDs())
	}
	// The next generation starts a new window and finds the later call.
	h.scanAll()
	if len(h.works) != 5 || len(h.absent) != 0 {
		t.Fatalf("next generation: works %v absent %v", h.workIDs(), h.absent)
	}
}

func TestShiftingPagesOnlyDelay(t *testing.T) {
	c := &clock{start}
	var calls []jiminny.Activity
	for i := 0; i < 6; i++ {
		calls = append(calls, jiminnytest.Call(fmt.Sprintf("c%d", i), "Weekly", start.Add(-time.Duration(i+1)*time.Hour)))
	}
	fake := jiminnytest.New(calls...)
	defer fake.Close()
	h := newHost(t, newIngress(t, fake, c, jiminny.Config{PageSize: 2}))
	// A newly processed call lands at the top while the scan is on page 2,
	// pushing everything down a place: one item repeats, none is skipped
	// for good.
	fake.Insert = func(page int) {
		if page == 2 {
			fake.Insert = nil
			fake.Put(jiminnytest.Call("fresh", "Weekly", start.Add(-time.Minute)))
		}
	}
	h.scanAll()
	h.scanAll()
	if len(h.works) != 7 || len(h.accepted) != 7 {
		t.Fatalf("works %v, %d events", h.workIDs(), len(h.accepted))
	}
}

func TestFilters(t *testing.T) {
	c := &clock{start}
	workshop := jiminnytest.Call("w", "Client workshop", start.Add(-time.Hour))
	workshop.ActivityType = "Workshop"
	sms := jiminnytest.Call("s", "Text", start.Add(-time.Hour))
	sms.Type = "sms-inbound"
	pending := jiminnytest.Call("p", "Still processing", start.Add(-time.Hour))
	pending.Status = "in-progress"
	fake := jiminnytest.New(jiminnytest.Call("a", "Weekly progress", start.Add(-time.Hour)), workshop, sms, pending)
	defer fake.Close()

	h := newHost(t, newIngress(t, fake, c, jiminny.Config{}))
	h.scanAll()
	if fmt.Sprint(h.workIDs()) != "[a w]" {
		t.Fatalf("defaults: %v", h.workIDs())
	}
	h = newHost(t, newIngress(t, fake, c, jiminny.Config{ActivityTypes: []string{"workshop"}}))
	h.scanAll()
	if fmt.Sprint(h.workIDs()) != "[w]" {
		t.Fatalf("activity_types: %v", h.workIDs())
	}
	h = newHost(t, newIngress(t, fake, c, jiminny.Config{TitleContains: []string{"PROGRESS"}}))
	h.scanAll()
	if fmt.Sprint(h.workIDs()) != "[a]" {
		t.Fatalf("title_contains: %v", h.workIDs())
	}
	// A call still processing is found once Jiminny finishes it.
	pending.Status = "completed"
	fake.Put(pending)
	h = newHost(t, newIngress(t, fake, c, jiminny.Config{}))
	h.scanAll()
	if fmt.Sprint(h.workIDs()) != "[a p w]" {
		t.Fatalf("after processing: %v", h.workIDs())
	}
}

func TestUnbuiltActivitiesAreRetried(t *testing.T) {
	c := &clock{start}
	fake := jiminnytest.New(jiminnytest.Call("a", "Weekly", start.Add(-time.Hour)))
	defer fake.Close()
	h := newHost(t, newIngress(t, fake, c, jiminny.Config{}))
	h.scanAll()
	// Jiminny cannot build "a" or the new "b" this time.
	fake.Delete("a")
	fake.SetFailed("a", "b")
	h.scanAll()
	fake.SetFailed()
	fake.Put(jiminnytest.Call("a", "Weekly", start.Add(-time.Hour)))
	fake.Put(jiminnytest.Call("b", "Weekly", start.Add(-30*time.Minute)))
	h.scanAll()
	if fmt.Sprint(h.workIDs()) != "[a b]" || len(h.accepted) != 2 {
		t.Fatalf("works %v, %d events", h.workIDs(), len(h.accepted))
	}
}

func TestActivityWithoutTimeFailsThePage(t *testing.T) {
	c := &clock{start}
	fake := jiminnytest.New()
	defer fake.Close()
	in := newIngress(t, fake, c, jiminny.Config{})
	if _, err := in.Push(context.Background(), source, extension.Delivery{ID: "d", ReceivedAt: start, Body: []byte(`{"id":"x","type":"conference","status":"completed"}`)}); !errors.Is(err, jiminny.ErrNoTime) {
		t.Fatalf("got %v", err)
	}
}

func TestErrorsCarryNoSecrets(t *testing.T) {
	c := &clock{start}
	fake := jiminnytest.New()
	defer fake.Close()
	in := newIngress(t, fake, c, jiminny.Config{})
	in.Client.Token = func(context.Context) (string, error) { return "wrong-secret-value", nil }
	_, err := in.Reconcile(context.Background(), extension.Scan{Source: source})
	if !errors.Is(err, jiminny.ErrUnauthorized) || strings.Contains(err.Error(), "wrong-secret-value") {
		t.Fatalf("got %v", err)
	}
}
