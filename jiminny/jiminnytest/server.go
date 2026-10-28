// Package jiminnytest is a fake of Jiminny's /getActivities for tests and
// certification. It holds synthetic activities only.
package jiminnytest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/open-conduit/open-conduit-jiminny/jiminny"
)

// Token is the only API token the fake accepts.
const Token = "fixture-token"

// Server is a fake Jiminny customer API.
type Server struct {
	*httptest.Server
	mu         sync.Mutex
	activities map[string]jiminny.Activity
	failed     []string
	faults     []int
	requests   int
	// Insert, when set, runs before each page is answered (to change the
	// data under a scan in progress).
	Insert func(page int)
}

// New starts a fake with the given activities.
func New(activities ...jiminny.Activity) *Server {
	s := &Server{activities: map[string]jiminny.Activity{}}
	for _, a := range activities {
		s.activities[a.ID] = a
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// Put adds or replaces an activity.
func (s *Server) Put(a jiminny.Activity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activities[a.ID] = a
}

// Delete removes an activity.
func (s *Server) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.activities, id)
}

// Fail answers the next requests with these statuses, in order.
func (s *Server) Fail(statuses ...int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults = append(s.faults, statuses...)
}

// SetFailed sets the IDs every page reports as not built.
func (s *Server) SetFailed(ids ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failed = ids
}

// Requests is how many requests the fake has answered.
func (s *Server) Requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests++
	var fault int
	if len(s.faults) > 0 {
		fault, s.faults = s.faults[0], s.faults[1:]
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Authorization") != "Bearer "+Token {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Unauthenticated."}}`))
		return
	}
	if fault != 0 {
		w.WriteHeader(fault)
		_, _ = w.Write([]byte(`{"message":"Too Many Attempts."}`))
		return
	}
	if r.URL.Path != "/getActivities" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	from, err1 := time.ParseInLocation(jiminny.QueryTime, q.Get("fromDate"), time.UTC)
	to, err2 := time.ParseInLocation(jiminny.QueryTime, q.Get("toDate"), time.UTC)
	page, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("pageSize"))
	if err1 != nil || err2 != nil || !from.Before(to) || to.Sub(from) > 183*24*time.Hour || page < 1 || size < 1 || size > 500 {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":{"range":"invalid"}}`))
		return
	}
	if s.Insert != nil {
		s.Insert(page)
	}
	s.mu.Lock()
	var match []jiminny.Activity
	for _, a := range s.activities {
		at, err := time.Parse(time.RFC3339, a.ActualStartTime)
		if a.Status != q.Get("status") || err != nil || at.Before(from) || at.After(to) {
			continue
		}
		match = append(match, a)
	}
	failed := append([]string(nil), s.failed...)
	s.mu.Unlock()
	sort.Slice(match, func(i, j int) bool {
		if match[i].ActualStartTime != match[j].ActualStartTime {
			return match[i].ActualStartTime > match[j].ActualStartTime
		}
		return match[i].ID < match[j].ID
	})
	maxPage := (len(match) + size - 1) / size
	if page > maxPage {
		page = maxPage
	}
	var results []jiminny.Activity
	if page > 0 {
		end := min(page*size, len(match))
		results = match[(page-1)*size : end]
	}
	out := map[string]any{"results": append([]jiminny.Activity{}, results...), "metadata": map[string]any{"page": page, "pageSize": size, "maxPage": maxPage}, "failed": append([]string{}, failed...)}
	_ = json.NewEncoder(w).Encode(out)
}

// Call is a synthetic processed meeting that started at t.
func Call(id, title string, t time.Time) jiminny.Activity {
	return jiminny.Activity{ID: id, Type: "conference", ActivityType: "Weekly", Title: title, Status: "completed",
		CreatedAt: t.UTC().Format(time.RFC3339), ActualStartTime: t.UTC().Format(time.RFC3339), UpdatedAt: t.UTC().Format(time.RFC3339)}
}
