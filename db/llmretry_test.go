package db

import (
	"testing"
	"time"
)

// A profile's retry override must survive a full round trip through the real
// column list — this is what catches a mis-ordered scan/insert after adding six
// columns at once. Also pins the "empty api key on update keeps the row usable"
// path, since that UPDATE has its own parameter numbering.
func TestProfileRetryRoundTrip(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	want := RetryOverride{
		Connect: RetryRule{Attempts: 5, IntervalMS: 2000},
		Empty:   RetryRule{Attempts: -1},
		Stream:  RetryRule{IntervalMS: 250},
	}
	id, err := d.SaveProfile(&LLMProfile{
		Name: "t-retry-roundtrip", Format: "openai", Model: "m", APIKey: "k", Retry: want,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM llm_profiles WHERE id=$1`, id) })

	got, err := d.ProfileByID(id)
	if err != nil || got == nil {
		t.Fatalf("ProfileByID: %v", err)
	}
	if got.Retry != want {
		t.Fatalf("retry=%+v, want %+v", got.Retry, want)
	}
	// Keyless update path (the UI sends no key when the user didn't retype it).
	got.APIKey = ""
	got.Retry.Stream = RetryRule{Attempts: 3, IntervalMS: 700}
	if _, err := d.SaveProfile(got); err != nil {
		t.Fatal(err)
	}
	after, err := d.ProfileByID(id)
	if err != nil || after == nil {
		t.Fatalf("ProfileByID after update: %v", err)
	}
	if after.Retry.Stream != (RetryRule{Attempts: 3, IntervalMS: 700}) {
		t.Fatalf("stream=%+v after update", after.Retry.Stream)
	}
	if after.Retry.Connect != want.Connect || after.Retry.Empty != want.Empty {
		t.Fatalf("untouched rules changed: %+v", after.Retry)
	}
	// The listing query reads a different column list — it must agree.
	profiles, err := d.ListProfiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range profiles {
		if p.ID == id && p.Retry.Connect != want.Connect {
			t.Fatalf("ListProfiles retry=%+v, want %+v", p.Retry, want)
		}
	}
}

// Out-of-range values are clamped on the way in, so the DB CHECK constraint is
// never what the user hears about.
func TestProfileRetryClampedOnSave(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	id, err := d.SaveProfile(&LLMProfile{
		Name: "t-retry-clamp", Format: "openai", Model: "m", APIKey: "k",
		Retry: RetryOverride{Connect: RetryRule{Attempts: -99, IntervalMS: -5}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM llm_profiles WHERE id=$1`, id) })
	got, err := d.ProfileByID(id)
	if err != nil || got == nil {
		t.Fatalf("ProfileByID: %v", err)
	}
	if got.Retry.Connect != (RetryRule{Attempts: -1}) {
		t.Fatalf("connect=%+v, want attempts -1 and no interval", got.Retry.Connect)
	}
}

// The global policy round-trips through settings, and an unset key reads back as
// "everything on its built-in default".
func TestLLMRetryPolicyRoundTrip(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	before, hadBefore, err := d.GetSetting(settingLLMRetryPolicy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if hadBefore {
			d.SetSetting(settingLLMRetryPolicy, before)
		} else {
			d.Exec(`DELETE FROM settings WHERE key=$1`, settingLLMRetryPolicy)
		}
	})

	want := LLMRetryPolicy{
		Connect: RetryRule{Attempts: 4, IntervalMS: 1500},
		Breaker: RetryRule{Attempts: 2, IntervalMS: 90_000},
		Intent:  RetryRule{Attempts: -1},
	}
	if err := d.SetLLMRetryPolicy(want); err != nil {
		t.Fatal(err)
	}
	got := d.LLMRetryPolicy()
	if got != want {
		t.Fatalf("policy=%+v, want %+v", got, want)
	}
	if d := got.Breaker.Interval(); d != 90*time.Second {
		t.Fatalf("breaker interval=%v, want 90s", d)
	}

	// 越界值写进去也会被夹回区间，读出来是夹紧后的值。
	if err := d.SetLLMRetryPolicy(LLMRetryPolicy{Stream: RetryRule{Attempts: 999, IntervalMS: 99_999_999}}); err != nil {
		t.Fatal(err)
	}
	if got := d.LLMRetryPolicy().Stream; got.Attempts != 20 || got.IntervalMS != 3600_000 {
		t.Fatalf("stream=%+v, want the 20 / 1h caps", got)
	}

	// 键不存在 = 全默认。
	if _, err := d.Exec(`DELETE FROM settings WHERE key=$1`, settingLLMRetryPolicy); err != nil {
		t.Fatal(err)
	}
	if got := d.LLMRetryPolicy(); got != (LLMRetryPolicy{}) {
		t.Fatalf("unset policy=%+v, want zero", got)
	}
}
