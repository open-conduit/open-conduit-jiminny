package jiminny

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/open-conduit/open-conduit/domain"
	"github.com/open-conduit/open-conduit/extension"
)

const (
	// Name is the extension's registration name, entry and route type.
	Name = "jiminny"
	// Processed is the event family: a call or meeting Jiminny has
	// finished processing.
	Processed = "jiminny.activity.processed"
	// Schema is the processed event's payload schema.
	Schema = "jiminny.activity.v1"
	// Kind is the kind of Work one processed call becomes.
	Kind = "jiminny.call"
)

// Payload is the processed event's data: enough for an agent to fetch the
// call with its own tools, never the transcript.
type Payload struct {
	ActivityID   string `json:"activity_id"`
	Title        string `json:"title,omitempty"`
	ActivityType string `json:"activity_type,omitempty"`
	Type         string `json:"type"`
	OccurredAt   string `json:"occurred_at"`
}

// Ingress discovers processed calls for one entry. Every scan lists the whole
// lookback window and ends complete, so the host diffs it against the last
// complete scan. Its only family is re-derivable: whatever push is missing,
// a scan finds again.
type Ingress struct {
	Config Config
	Client *Client
	Now    func() time.Time
	// Log receives safe diagnostics (counts and codes, never data).
	Log *log.Logger
}

// Families are the source's declared families.
func Families() []extension.EventFamily {
	return []extension.EventFamily{{Type: Processed, ReDerivable: true}}
}

func (in *Ingress) Spec() extension.IngressSpec { return extension.IngressSpec{Families: Families()} }

// Push normalizes one activity, as /getActivity returns it, into the same
// observation a scan produces. Nothing calls it yet (push is out of scope);
// it fixes identity so a later webhook cannot fork Work.
func (in *Ingress) Push(_ context.Context, source extension.Source, d extension.Delivery) (extension.PushResult, error) {
	var a Activity
	if err := json.Unmarshal(d.Body, &a); err != nil || a.ID == "" {
		return extension.PushResult{}, fmt.Errorf("%w: %v", extension.ErrInvalid, ErrResponse)
	}
	if !in.Config.Keep(a) {
		return extension.PushResult{}, nil
	}
	o, err := Observe(source, a, d.ReceivedAt)
	if err != nil {
		return extension.PushResult{}, err
	}
	return extension.PushResult{Observations: []extension.Observation{o}}, nil
}

// Reconcile fetches one page of the scan's window. The window is fixed when
// the scan starts and carried in its ScanID, so every page of one
// generation asks the same question.
func (in *Ingress) Reconcile(ctx context.Context, scan extension.Scan) (extension.Snapshot, error) {
	now := in.now()
	from, to, err := window(scan.ScanID)
	scanID := scan.ScanID
	if scanID == "" {
		to = now.UTC().Truncate(time.Second)
		from = to.Add(-in.Config.Lookback())
		scanID = fmt.Sprintf("window/%d/%d", from.Unix(), to.Unix())
	} else if err != nil {
		return extension.Snapshot{}, err
	}
	page := 1
	if scan.Cursor != "" {
		if page, err = strconv.Atoi(scan.Cursor); err != nil || page < 2 {
			return extension.Snapshot{}, fmt.Errorf("%w: unreadable scan cursor", extension.ErrInvalid)
		}
	}
	resp, err := in.Client.Activities(ctx, from, to, page, in.Config.PageSize)
	if err != nil {
		in.logf("page %d failed: %v", page, err)
		return extension.Snapshot{}, err
	}
	out := extension.Snapshot{ScanID: scanID}
	// A page past maxPage is answered with the last page again; the scan
	// has already seen it, so it ends here.
	if page > 1 && (int(resp.Metadata.Page) < page || resp.Metadata.MaxPage < page) {
		out.Complete, out.Checkpoint = true, to.Format(time.RFC3339)
		return out, nil
	}
	if len(resp.Failed) > 0 {
		// Jiminny could not build these activities this time. A call that
		// fails is found by a later scan; one already seen keeps its Work
		// (its absence is ignored by the route) and is never repeated.
		in.logf("page %d: Jiminny could not build %d activities; a later scan retries them", page, len(resp.Failed))
	}
	seen := map[string]bool{}
	for _, a := range resp.Results {
		if a.ID == "" || seen[a.ID] || !in.Config.Keep(a) {
			continue
		}
		seen[a.ID] = true
		o, err := Observe(extension.Source{Namespace: scan.Source.Namespace, ScopeID: scan.Source.ScopeID}, a, now)
		if err != nil {
			in.logf("page %d: %v", page, err)
			return extension.Snapshot{}, err
		}
		out.Observations = append(out.Observations, o)
	}
	if resp.Metadata.MaxPage <= page {
		out.Complete, out.Checkpoint = true, to.Format(time.RFC3339)
	} else {
		out.NextCursor = strconv.Itoa(page + 1)
	}
	return out, nil
}

// ErrNoTime is an activity with no time to stamp it with. It fails the page
// rather than drop the call.
var ErrNoTime = errors.New("jiminny: an activity has no start or creation time")

// Observe is the canonical observation of one processed activity. Its item
// is the activity's ID; its version hashes the payload, so a changed title
// is a new version of the same call (and the same Work), never a new call.
func Observe(source extension.Source, a Activity, receivedAt time.Time) (extension.Observation, error) {
	occurred, ok := parseTime(a.ActualStartTime)
	if !ok {
		if occurred, ok = parseTime(a.CreatedAt); !ok {
			if occurred, ok = parseTime(a.ScheduledStartTime); !ok {
				return extension.Observation{}, ErrNoTime
			}
		}
	}
	p := Payload{ActivityID: a.ID, Title: a.Title, ActivityType: a.ActivityType, Type: a.Type, OccurredAt: occurred.Format(time.RFC3339)}
	data, err := json.Marshal(p)
	if err != nil {
		return extension.Observation{}, err
	}
	sum := sha256.Sum256(append([]byte(Schema+"\n"), data...))
	version := hex.EncodeToString(sum[:12])
	return extension.Observation{ID: a.ID, Version: version, Event: domain.Event{
		ID:             domain.ID("jiminny/" + a.ID + "/" + version),
		Type:           Processed,
		Source:         source.Namespace,
		DeliveryID:     "activity/" + a.ID + "/" + version,
		CorrelationKey: Key(source, a.ID),
		OccurredAt:     occurred,
		ReceivedAt:     receivedAt,
		Data:           domain.Payload{Schema: Schema, Data: data},
	}}, nil
}

// Key is the correlation key one call's Work owns.
func Key(source extension.Source, activityID string) *domain.CorrelationKey {
	return &domain.CorrelationKey{Source: source.Namespace, ExternalID: activityID}
}

func window(scanID string) (time.Time, time.Time, error) {
	parts := strings.Split(scanID, "/")
	if len(parts) != 3 || parts[0] != "window" {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: unreadable scan generation", extension.ErrInvalid)
	}
	from, err1 := strconv.ParseInt(parts[1], 10, 64)
	to, err2 := strconv.ParseInt(parts[2], 10, 64)
	if err1 != nil || err2 != nil || from >= to {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: unreadable scan generation", extension.ErrInvalid)
	}
	return time.Unix(from, 0).UTC(), time.Unix(to, 0).UTC(), nil
}

func (in *Ingress) now() time.Time {
	if in.Now != nil {
		return in.Now()
	}
	return time.Now()
}

func (in *Ingress) logf(format string, args ...any) {
	if in.Log != nil {
		in.Log.Printf(format, args...)
	}
}
