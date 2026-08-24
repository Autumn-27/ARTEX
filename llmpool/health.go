// Package llmpool implements LLM failover ("轮询"): a Provider decorator that
// walks an ordered chain of LLM profiles and moves to the next one when the
// current one can't serve the request — out of credit, revoked key, rate-limited
// past the SDK's own retries, or down.
//
// The chain is built by the server from llm_profiles (active profile first, then
// by priority), so the whole process shares ONE chain and ONE circuit-breaker
// registry: when a task discovers a profile is out of credit, every other task
// skips it immediately.
package llmpool

import (
	"sync"
	"time"
)

// backoff is the cooling-off ladder, indexed by trip count: the 1st trip cools
// for a minute, the 2nd for five, everything after that for half an hour. A
// profile that is merely rate-limited recovers quickly; one that keeps failing
// stops being probed every round.
var backoff = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute}

// State is one profile's circuit-breaker state.
type State struct {
	Fails     int       // consecutive failures; reset by a success
	Trips     int       // total trips, indexes the backoff ladder
	OpenUntil time.Time // zero / past = closed (usable)
	LastError string
	LastAt    time.Time
}

// Open reports whether the breaker is currently open (profile should be skipped).
func (s State) Open() bool { return time.Now().Before(s.OpenUntil) }

// softTripAfter is how many consecutive TRANSIENT failures (429 / 5xx / network)
// trip the breaker. Deterministic failures (no credit, bad key) trip on the first.
const softTripAfter = 3

// Registry holds the circuit-breaker state of every profile, keyed by profile id.
// It is process-wide and outlives any single Pool instance, so rebuilding the pool
// (saving an unrelated profile, toggling a setting) never clears what we've learned
// about which backends are broken.
type Registry struct {
	mu sync.Mutex
	m  map[int64]*State

	// persist / forget mirror state to the DB so a cooling-off window survives a
	// restart. Both may be nil (no DB); both are called off the hot path.
	persist func(id int64, st State)
	forget  func(id int64)
}

// NewRegistry builds an empty registry. persist/forget may be nil.
func NewRegistry(persist func(id int64, st State), forget func(id int64)) *Registry {
	return &Registry{m: map[int64]*State{}, persist: persist, forget: forget}
}

// Restore seeds state loaded from the DB at startup (bypasses persistence).
func (r *Registry) Restore(id int64, st State) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := st
	r.m[id] = &cp
}

// Get returns a copy of one profile's state.
func (r *Registry) Get(id int64) State {
	r.mu.Lock()
	defer r.mu.Unlock()
	if st := r.m[id]; st != nil {
		return *st
	}
	return State{}
}

// IsOpen reports whether a profile is in its cooling-off window.
func (r *Registry) IsOpen(id int64) bool { return r.Get(id).Open() }

// Pass records a successful call: clears the failure counters so a profile that
// recovers is fully trusted again (and drops the persisted row).
func (r *Registry) Pass(id int64) {
	r.mu.Lock()
	st := r.m[id]
	if st == nil || (st.Fails == 0 && st.Trips == 0 && st.OpenUntil.IsZero()) {
		r.mu.Unlock()
		return // already clean — nothing to write
	}
	delete(r.m, id)
	forget := r.forget
	r.mu.Unlock()
	if forget != nil {
		forget(id)
	}
}

// Trip records a failed call. hard=true marks a deterministic failure (no credit,
// invalid key, missing model) which opens the breaker immediately; hard=false is a
// transient one (429 / 5xx / network) that needs softTripAfter in a row. Returns
// true when this call is what opened the breaker, so the caller can log it once.
func (r *Registry) Trip(id int64, errMsg string, hard bool) (tripped bool) {
	r.mu.Lock()
	st := r.m[id]
	if st == nil {
		st = &State{}
		r.m[id] = st
	}
	st.Fails++
	st.LastError = errMsg
	st.LastAt = time.Now()
	if hard || st.Fails >= softTripAfter {
		st.Trips++
		d := backoff[len(backoff)-1]
		if st.Trips-1 < len(backoff) {
			d = backoff[st.Trips-1]
		}
		st.OpenUntil = time.Now().Add(d)
		st.Fails = 0 // counted into this trip; start fresh for the half-open probe
		tripped = true
	}
	snap := *st
	persist := r.persist
	r.mu.Unlock()
	if persist != nil {
		persist(id, snap)
	}
	return tripped
}

// Reset clears one profile's state — the UI's "立即恢复" action.
func (r *Registry) Reset(id int64) {
	r.mu.Lock()
	delete(r.m, id)
	forget := r.forget
	r.mu.Unlock()
	if forget != nil {
		forget(id)
	}
}

// Snapshot returns a copy of all tracked state, for the status API.
func (r *Registry) Snapshot() map[int64]State {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[int64]State, len(r.m))
	for id, st := range r.m {
		out[id] = *st
	}
	return out
}
