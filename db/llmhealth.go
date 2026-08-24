package db

import "time"

// LLM failover health: one row per profile tracking its circuit-breaker state.
// The authoritative copy lives in memory (llmpool.Registry); this table only
// survives a restart so a cooling-off window isn't silently reset by one.

// LLMHealth is one profile's circuit-breaker state.
type LLMHealth struct {
	ProfileID int64      `json:"profile_id"`
	Fails     int        `json:"fails"`      // consecutive failures; cleared on success
	Trips     int        `json:"trips"`      // total trips, drives the backoff ladder
	OpenUntil *time.Time `json:"open_until"` // nil/past = closed (healthy)
	LastError string     `json:"last_error"`
	LastAt    time.Time  `json:"last_at"`
}

// LoadLLMHealth returns the profiles still in an UNEXPIRED cooling-off window.
// Expired rows are deliberately skipped: after a restart a profile that has
// finished cooling should be treated as healthy again and re-probed on its next
// call, not resurrected as broken.
func (d *DB) LoadLLMHealth() ([]LLMHealth, error) {
	rows, err := d.Query(`SELECT profile_id,fails,trips,open_until,COALESCE(last_error,''),last_at
FROM llm_profile_health WHERE open_until IS NOT NULL AND open_until > now()`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LLMHealth
	for rows.Next() {
		var h LLMHealth
		if err := rows.Scan(&h.ProfileID, &h.Fails, &h.Trips, &h.OpenUntil, &h.LastError, &h.LastAt); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// SaveLLMHealth upserts one profile's circuit-breaker state.
func (d *DB) SaveLLMHealth(h LLMHealth) error {
	_, err := d.Exec(`
INSERT INTO llm_profile_health(profile_id,fails,trips,open_until,last_error,last_at)
VALUES ($1,$2,$3,$4,$5,now())
ON CONFLICT (profile_id) DO UPDATE SET
  fails=EXCLUDED.fails, trips=EXCLUDED.trips, open_until=EXCLUDED.open_until,
  last_error=EXCLUDED.last_error, last_at=now()`,
		h.ProfileID, h.Fails, h.Trips, h.OpenUntil, h.LastError)
	return err
}

// ClearLLMHealth drops one profile's state (manual "recover now" from the UI).
func (d *DB) ClearLLMHealth(profileID int64) error {
	_, err := d.Exec(`DELETE FROM llm_profile_health WHERE profile_id=$1`, profileID)
	return err
}
