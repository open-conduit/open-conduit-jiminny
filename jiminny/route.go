package jiminny

import (
	"context"

	"github.com/open-conduit/open-conduit/domain"
	"github.com/open-conduit/open-conduit/extension"
)

// Route makes one Work per processed call, the first time the call is seen.
// Everything after that is history, not work: a re-observed or edited call
// (a new version on a key that Work already owns), a call ageing out of the
// window (core.ingress.absent) and anything for completed Work match
// nothing, so they never restart a call or wake one waiting for review.
type Route struct{}

func (Route) Match(_ context.Context, in extension.RouteInput) (extension.RouteDecision, error) {
	e := in.Event.Event
	if e.Type != Processed || e.CorrelationKey == nil || in.Work != nil {
		return extension.RouteDecision{}, nil
	}
	return extension.RouteDecision{Match: true, CreateID: WorkID(in.Scope.ID, e.CorrelationKey.ExternalID), Kind: Kind}, nil
}

// WorkID is the ID of a call's Work in a scope.
func WorkID(scope domain.ID, activityID string) domain.ID {
	return domain.ID("jiminny/" + string(scope) + "/" + activityID)
}
