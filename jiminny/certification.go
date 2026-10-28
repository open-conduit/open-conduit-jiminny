package jiminny

import (
	"encoding/json"
	"time"

	"github.com/open-conduit/open-conduit/domain"
	"github.com/open-conduit/open-conduit/extension"
	"github.com/open-conduit/open-conduit/protocol"
)

// Certification is the package's certification.json: a fixture command
// that serves a fake Jiminny for the run.
func Certification() protocol.CertificationFile {
	return protocol.CertificationFile{Command: []string{Command, "certify-fixture"}}
}

// FixtureScope and FixtureEntry name the certification fixture's entry.
const (
	FixtureScope = "jiminny-scope"
	FixtureEntry = "calls"
)

// FixtureSource is the fixture entry's source.
var FixtureSource = extension.Source{Namespace: Name + "/" + FixtureScope + "/" + FixtureEntry, ScopeID: FixtureScope}

// NewFixture is the certification fixture over a fake Jiminny at baseURL
// holding activity (and at least one more, so the scan pages): ingress
// identity shared by push and reconcile, and the route's three decisions.
func NewFixture(baseURL, token string, activity Activity, at time.Time) (protocol.Fixture, error) {
	cfg, _ := json.Marshal(Config{Scope: FixtureScope, BaseURL: baseURL, PageSize: 1})
	body, _ := json.Marshal(activity)
	d := extension.Delivery{ID: "push-" + activity.ID, ReceivedAt: at, ContentType: "application/json", Body: body}
	o, err := Observe(FixtureSource, activity, at)
	if err != nil {
		return protocol.Fixture{}, err
	}
	ev := o.Event
	ev.ID = "certify-call"
	scope := domain.Scope{ID: FixtureScope, Name: FixtureScope}
	route := domain.Route{Name: "calls", Handler: Name, Binding: "agent"}
	first := extension.RouteInput{Event: domain.AcceptedEvent{Event: ev, Sequence: 1}, Scope: scope, Route: route}
	owner := domain.Work{ID: WorkID(FixtureScope, activity.ID), ScopeID: FixtureScope, Kind: Kind, State: domain.WorkWaiting, CorrelationKeys: []domain.CorrelationKey{*ev.CorrelationKey}}
	again := first
	again.Event.Sequence = 2
	again.Work = &owner
	absentData, _ := json.Marshal(map[string]string{"source": FixtureSource.Namespace, "item": activity.ID, "last_version": o.Version})
	absent := extension.RouteInput{Event: domain.AcceptedEvent{Event: domain.Event{ID: "certify-absent", Type: "core.ingress.absent", Source: "core.ingress", DeliveryID: "core.absent/certify",
		CorrelationKey: ev.CorrelationKey, OccurredAt: at, ReceivedAt: at, Data: domain.Payload{Schema: "core.ingress.absent.v1", Data: absentData}}, Sequence: 3}, Scope: scope, Route: route, Work: &owner}
	return protocol.Fixture{
		Entries: []protocol.ConfigEntry{{ScopeID: FixtureScope, Type: Name, Name: FixtureEntry, Config: cfg,
			Connection: &protocol.ConnectionEntry{ID: Name, Type: Name, Secrets: []string{TokenSecret}}}},
		Secrets: map[string]string{TokenSecret: token},
		Ingress: &protocol.IngressCase{Source: FixtureSource.Namespace, Delivery: protocol.FromDelivery(d),
			Canonical: protocol.FromPushResult(extension.PushResult{Observations: []extension.Observation{o}}).Observations[0], ReceiveTimeVaries: true},
		Routes: []protocol.RouteCase{
			{Handler: Name, Input: protocol.FromRouteInput(first), Want: protocol.RouteDecision{Match: true, CreateID: string(owner.ID), Kind: Kind}},
			{Handler: Name, Input: protocol.FromRouteInput(again), Want: protocol.RouteDecision{}},
			{Handler: Name, Input: protocol.FromRouteInput(absent), Want: protocol.RouteDecision{}},
		},
	}, nil
}
