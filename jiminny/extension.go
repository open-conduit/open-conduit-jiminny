package jiminny

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/open-conduit/open-conduit/domain"
	"github.com/open-conduit/open-conduit/extension"
	"github.com/open-conduit/open-conduit/protocol"
	"github.com/open-conduit/open-conduit/protocol/sdk"
)

// Version is this release, set at build time.
var Version = "0.1.0"

// Command is the package's executable.
const Command = "jiminny-extension"

// Manifest is the package's open-conduit-extension.json.
func Manifest() protocol.Manifest {
	return protocol.Manifest{Protocol: []int{protocol.Version}, Name: Name, Version: Version,
		Summary: "Discovers calls Jiminny has finished processing, by periodic reconciliation, as one Work per call.",
		Command: []string{Command},
		Provides: protocol.Provides{
			Connections: []protocol.TypeDecl{{Type: Name}},
			Extensions:  []protocol.ExtensionDecl{{Type: Name, Ingress: true}},
			Families:    []protocol.Family{{Type: Processed, ReDerivable: true}},
			Schemas:     []protocol.SchemaDecl{{Name: "jiminny.activity", Versions: []int{1}}},
			Routes:      []protocol.RouteDecl{{Handler: Name}},
			WorkKinds:   []protocol.WorkKindDecl{{Kind: Kind}},
			Checks:      []protocol.CheckDecl{{Name: Name}},
			Render:      true,
		}}
}

// Options are the process's dependencies.
type Options struct {
	Now func() time.Time
	Log *log.Logger
}

// Build returns the served extension.
func Build(o Options) *sdk.Extension {
	var (
		mu      sync.Mutex
		sources = map[string]*Ingress{}
		host    atomic.Pointer[sdk.Host]
	)
	ext := &sdk.Extension{Manifest: Manifest()}
	ext.Validate = func(_ context.Context, p protocol.ValidateParams) error {
		if p.Type != Name {
			return nil
		}
		switch p.Kind {
		case protocol.ValidateExtension:
			_, err := ParseConfig(p.Config)
			return err
		case protocol.ValidateConnection:
			var c map[string]json.RawMessage
			if len(p.Config) > 0 && (json.Unmarshal(p.Config, &c) != nil || len(c) > 0) {
				return errors.New("a jiminny connection takes no config; its token is the secret_refs entry \"token\"")
			}
		}
		return nil
	}
	ext.Configure = func(_ context.Context, h *sdk.Host, p protocol.ConfigureParams) (protocol.ConfigureResult, error) {
		host.Store(h)
		var out protocol.ConfigureResult
		next := map[string]*Ingress{}
		for _, e := range p.Entries {
			if e.Type != Name {
				continue
			}
			cfg, err := ParseConfig(e.Config)
			if err != nil {
				return out, protocol.Errorf(protocol.CodeInvalid, err.Error())
			}
			if cfg.Scope != "" && cfg.Scope != e.ScopeID {
				return out, protocol.Errorf(protocol.CodeInvalid, "a jiminny entry's scope must be its containing scope")
			}
			if e.Connection == nil || e.Connection.Type != Name {
				return out, protocol.Errorf(protocol.CodeInvalid, "a jiminny entry names a jiminny connection holding the API token")
			}
			scope, conn := domain.ID(e.ScopeID), e.Connection.ID
			name := Name + "/" + e.ScopeID + "/" + e.Name
			token := func(ctx context.Context) (string, error) {
				h := host.Load()
				if h == nil {
					return "", ErrUnauthorized
				}
				values, err := h.ConnectionSecrets(ctx, scope, conn, TokenSecret)
				if err != nil || values[TokenSecret] == "" {
					return "", errors.New("jiminny: the connection's token secret is not available")
				}
				return values[TokenSecret], nil
			}
			next[name] = &Ingress{Config: cfg, Client: &Client{BaseURL: cfg.URL(), Token: token}, Now: o.Now, Log: o.Log}
			out.Sources = append(out.Sources, protocol.SourceDecl{Name: name, Source: protocol.Source{Namespace: name, ScopeID: e.ScopeID},
				IntervalSeconds: int(cfg.Interval() / time.Second), Families: []protocol.EventFamily{{Type: Processed, ReDerivable: true}}})
		}
		mu.Lock()
		sources = next
		mu.Unlock()
		return out, nil
	}
	ext.Ingress = func(name string) (extension.Ingress, bool) {
		mu.Lock()
		defer mu.Unlock()
		in, ok := sources[name]
		return in, ok
	}
	ext.Routes = map[string]extension.RouteHandler{Name: Route{}}
	ext.Render = Render
	ext.Checks = map[string]func(context.Context, *sdk.Host, bool) (string, error){Name: func(ctx context.Context, _ *sdk.Host, deep bool) (string, error) {
		mu.Lock()
		names := make([]string, 0, len(sources))
		for name := range sources {
			names = append(names, name)
		}
		current := sources
		mu.Unlock()
		sort.Strings(names)
		if !deep {
			return fmt.Sprintf("%d source(s) configured", len(names)), nil
		}
		for _, name := range names {
			in := current[name]
			to := time.Now().UTC().Truncate(time.Second)
			if _, err := in.Client.Activities(ctx, to.Add(-24*time.Hour), to, 1, 1); err != nil {
				return "", fmt.Errorf("%s: %w", name, err)
			}
		}
		return fmt.Sprintf("%d source(s) reached Jiminny", len(names)), nil
	}}
	return ext
}

// Render shows a processed call in an agent's prompt.
func Render(e domain.Event) (string, bool) {
	if e.Type != Processed {
		return "", false
	}
	var p Payload
	if json.Unmarshal(e.Data.Data, &p) != nil || p.ActivityID == "" {
		return "", false
	}
	text := "### Jiminny call processed\n\n- Activity ID: `" + p.ActivityID + "`\n"
	if p.Title != "" {
		text += "- Title: " + p.Title + "\n"
	}
	if p.ActivityType != "" {
		text += "- Activity type: " + p.ActivityType + "\n"
	}
	text += "- Kind: " + p.Type + "\n- Started: " + p.OccurredAt + "\n"
	return text, true
}
