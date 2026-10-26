package chat

import (
	"net/http"
	"slices"
	"time"

	"github.com/bespinian/keera-chat/internal/gateway"
)

type orgOut struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type userOut struct {
	// ID and OrgID keep each person's conversations apart in a shared browser.
	ID      string   `json:"id"`
	Email   string   `json:"email"`
	Name    string   `json:"name"`
	Role    string   `json:"role"`
	OrgID   string   `json:"org_id"`
	OrgName string   `json:"org_name"`
	Orgs    []orgOut `json:"orgs"`
}

// modelOut is a model the person may chat with.
type modelOut struct {
	Alias       string `json:"alias"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	// Router picks a model per message.
	Router bool `json:"router,omitempty"`
	// Context is the model's context size in tokens. Routers have none.
	Context int `json:"context,omitempty"`
}

// keyOut is a key chat can send with. It never holds the secret.
type keyOut struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Project string   `json:"project,omitempty"`
	Models  []string `json:"models"`
	Budget  *budget  `json:"budget,omitempty"`
}

// budget is the limit closest to running out, so a person sees it coming.
type budget struct {
	Scope        string     `json:"scope"`
	Name         string     `json:"name,omitempty"`
	BudgetMicros int64      `json:"budget_micros"`
	SpendMicros  int64      `json:"spend_micros"`
	Period       string     `json:"period,omitempty"`
	ResetsAt     *time.Time `json:"resets_at,omitempty"`
}

// me returns the person, their keys and the models they may use.
func (s *Server) me(w http.ResponseWriter, r *http.Request, c *caller) {
	access, err := s.gw.Access(r.Context(), c.token)
	if err != nil {
		s.gatewayFailed(w, r, err)
		return
	}
	targets, err := s.chatTargets(r, c)
	if err != nil {
		s.gatewayFailed(w, r, err)
		return
	}
	keys, pending, inactive := usableKeys(access.Keys, targets)
	// Only the models some key can reach, in the gateway's order.
	offered := []modelOut{}
	for _, m := range targets {
		if slices.ContainsFunc(keys, func(k keyOut) bool { return slices.Contains(k.Models, m.Alias) }) {
			offered = append(offered, m)
		}
	}

	user := userOut{
		ID: c.me.UserID, Email: c.me.Email, Name: c.me.Name, Role: c.me.Role,
		OrgID: c.me.OrgID, OrgName: c.me.OrgName, Orgs: []orgOut{},
	}
	for _, o := range c.me.Orgs {
		// An invitation is accepted in the gateway, not here.
		if !o.Invited {
			user.Orgs = append(user.Orgs, orgOut{ID: o.ID, Name: o.Name})
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"user":          user,
		"gateway_url":   s.opts.GatewayURL,
		"keys":          keys,
		"models":        offered,
		"pending_keys":  pending,
		"inactive_keys": inactive,
	})
}

// chatTargets lists enabled chat models, then routers. Subscription models
// are left out: only Claude Code can use them.
func (s *Server) chatTargets(r *http.Request, c *caller) ([]modelOut, error) {
	models, err := s.gw.Models(r.Context(), c.token, c.me.OrgID)
	if err != nil {
		return nil, err
	}
	out := []modelOut{}
	for _, m := range models {
		if m.Enabled && m.Kind == "chat" && !m.Subscription {
			out = append(out, modelOut{
				Alias: m.Alias, Name: m.DisplayName,
				Description: m.Description, Context: m.MaxContext,
			})
		}
	}
	// Failing to read the routers still leaves the models.
	routers, err := s.gw.Routers(r.Context(), c.token, c.me.OrgID)
	if err != nil {
		if gateway.IsUnauthorized(err) {
			return nil, err
		}
		s.log.Warn("reading the routers failed", "error", err)
	}
	for _, rt := range routers {
		out = append(out, modelOut{
			Alias: rt.Alias, Name: rt.DisplayName,
			Description: rt.Description, Router: true,
		})
	}
	return out, nil
}

// usableKeys returns the active keys with their models, and counts the
// pending and inactive ones. Subscription keys only work from Claude Code.
func usableKeys(all []gateway.Key, targets []modelOut) (keys []keyOut, pending, inactive int) {
	keys = []keyOut{}
	for _, k := range all {
		if k.Kind == "subscription" {
			continue
		}
		switch k.State {
		case "active":
		case "pending":
			pending++
			continue
		default:
			inactive++
			continue
		}
		out := keyOut{
			ID: k.ID, Name: k.Name, Project: k.ProjectName, Models: []string{},
			Budget: tightest(k.Scopes),
		}
		if out.Name == "" {
			out.Name = k.Prefix
		}
		for _, m := range targets {
			if slices.Contains(k.AllowedModels, m.Alias) {
				out.Models = append(out.Models, m.Alias)
			}
		}
		keys = append(keys, out)
	}
	return keys, pending, inactive
}

// tightest is the budget with the least left, as a share of its size.
func tightest(scopes []gateway.Scope) *budget {
	var best *budget
	var bestLeft float64
	for _, sc := range scopes {
		if sc.BudgetMicros <= 0 {
			continue
		}
		left := 1 - float64(sc.SpendMicros)/float64(sc.BudgetMicros)
		if best == nil || left < bestLeft {
			bestLeft = left
			best = &budget{
				Scope: sc.Type, Name: sc.Name, BudgetMicros: sc.BudgetMicros,
				SpendMicros: sc.SpendMicros, Period: sc.Period, ResetsAt: sc.ResetsAt,
			}
		}
	}
	return best
}

// switchOrg moves the sign-in to another organisation.
func (s *Server) switchOrg(w http.ResponseWriter, r *http.Request, c *caller) {
	var in struct {
		OrgID string `json:"org_id"`
	}
	if !s.readJSON(w, r, &in) {
		return
	}
	if err := s.gw.SwitchOrg(r.Context(), c.token, in.OrgID); err != nil {
		s.gatewayFailed(w, r, err)
		return
	}
	s.forget(c.token)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
