package proxypool

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
)

// errUnknownSource is returned when a fetch targets a name not in BuiltinSources.
var errUnknownSource = errors.New("unknown proxy source")

// Config wires the pool's background loops to live settings via callbacks, so a
// settings change takes effect on the next tick without restarting the loops.
type Config struct {
	Enabled       func() bool          // master switch (proxy_pool_enabled)
	FetchInterval func() time.Duration // how often to pull enabled free sources
	CheckInterval func() time.Duration // how often to re-probe every enabled proxy
	ProbeURL      func() string        // liveness target (empty = DefaultProbeURL)
	Concurrency   int                  // parallel probes (default 50)
	ProbeTimeout  time.Duration        // per-probe timeout (default 10s)
}

// Pool runs the proxy pool's two background loops (fetch + probe) and serves
// one-off manual probes. It owns nothing the DB doesn't; all state lives in PG.
type Pool struct {
	db     *db.DB
	cfg    Config
	client *http.Client // for fetching source lists (direct, not through the pool)
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewPool builds a pool bound to db with the given config, applying defaults.
func NewPool(database *db.DB, cfg Config) *Pool {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 50
	}
	if cfg.ProbeTimeout <= 0 {
		cfg.ProbeTimeout = 10 * time.Second
	}
	return &Pool{
		db:     database,
		cfg:    cfg,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// Start launches the fetch and probe loops. Idempotent guards are the caller's
// job; call once. Stop() (or a cancelled parent ctx) ends both loops.
func (p *Pool) Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	p.cancel = cancel
	p.wg.Add(2)
	go p.loop(ctx, p.cfg.FetchInterval, 15*time.Minute, p.fetchOnce)
	go p.loop(ctx, p.cfg.CheckInterval, 30*time.Minute, p.probeOnce)
}

// Stop ends the background loops and waits for them to exit.
func (p *Pool) Stop() {
	if p.cancel != nil {
		p.cancel()
	}
	p.wg.Wait()
}

// loop runs work on a self-rescheduling timer whose interval is re-read each round
// (so settings changes apply next tick). It skips work while the pool is disabled.
func (p *Pool) loop(ctx context.Context, interval func() time.Duration, fallback time.Duration, work func(context.Context)) {
	defer p.wg.Done()
	next := func() time.Duration {
		d := fallback
		if interval != nil {
			if v := interval(); v > 0 {
				d = v
			}
		}
		if d < time.Minute {
			d = time.Minute // floor: never hammer sources/targets
		}
		return d
	}
	timer := time.NewTimer(next())
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if p.enabled() {
				work(ctx)
			}
			timer.Reset(next())
		}
	}
}

func (p *Pool) enabled() bool { return p.cfg.Enabled == nil || p.cfg.Enabled() }

func (p *Pool) probeURL() string {
	if p.cfg.ProbeURL == nil {
		return DefaultProbeURL
	}
	return p.cfg.ProbeURL()
}

// fetchOnce pulls every enabled free source and upserts new (untrusted) proxies,
// then immediately probes the freshly-added ones so they don't sit unknown for a
// full check interval.
func (p *Pool) fetchOnce(ctx context.Context) {
	names, err := p.db.Proxies().EnabledSources()
	if err != nil {
		log.Printf("[proxypool] list enabled sources: %v", err)
		return
	}
	for _, name := range names {
		_, _, _ = p.fetchSource(ctx, name)
	}
	// Probe whatever is now enabled+unknown so new nodes become usable fast.
	p.probeOnce(ctx)
}

// fetchSource pulls one source by name and upserts its proxies. Returns the total
// fetched and how many were newly added. Records the outcome regardless of the
// source's enabled state, so the manual "fetch now" button works on any source.
func (p *Pool) fetchSource(ctx context.Context, name string) (total, added int, err error) {
	store := p.db.Proxies()
	src, ok := sourceByName(name)
	if !ok {
		return 0, 0, errUnknownSource
	}
	proxies, ferr := fetch(ctx, src, p.client)
	if ferr != nil {
		_ = store.RecordFetch(name, 0, trimErr(ferr.Error()))
		return 0, 0, ferr
	}
	added, uerr := store.UpsertFromSource(name, proxies)
	if uerr != nil {
		_ = store.RecordFetch(name, len(proxies), trimErr(uerr.Error()))
		return len(proxies), 0, uerr
	}
	_ = store.RecordFetch(name, len(proxies), "")
	log.Printf("[proxypool] source %s: fetched %d, added %d new", name, len(proxies), added)
	return len(proxies), added, nil
}

// FetchSourceNow pulls one source on demand (manual "fetch now" button), then
// probes so freshly-added nodes become usable without waiting for the loop.
func (p *Pool) FetchSourceNow(ctx context.Context, name string) (total, added int, err error) {
	total, added, err = p.fetchSource(ctx, name)
	if err == nil && added > 0 {
		go p.probeOnce(context.WithoutCancel(ctx))
	}
	return total, added, err
}

// probeOnce concurrently probes every enabled proxy and writes back health.
func (p *Pool) probeOnce(ctx context.Context) {
	store := p.db.Proxies()
	proxies, err := store.ListProxies(db.ProxyFilter{OnlyEnabled: true})
	if err != nil {
		log.Printf("[proxypool] list for probe: %v", err)
		return
	}
	if len(proxies) == 0 {
		return
	}
	sem := make(chan struct{}, p.cfg.Concurrency)
	var wg sync.WaitGroup
	for _, pr := range proxies {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(pr *db.Proxy) {
			defer wg.Done()
			defer func() { <-sem }()
			res := Probe(ctx, pr.URL(), p.probeURL(), p.cfg.ProbeTimeout)
			_ = store.UpdateHealth(pr.ID, res.OK, int(res.Latency.Milliseconds()), res.Err)
		}(pr)
	}
	wg.Wait()
}

// ProbeNow probes a single proxy on demand (manual "check" button) and writes back
// the result, returning it for immediate UI feedback.
func (p *Pool) ProbeNow(ctx context.Context, id int64) (ProbeResult, error) {
	store := p.db.Proxies()
	pr, err := store.GetProxy(id)
	if err != nil {
		return ProbeResult{}, err
	}
	res := Probe(ctx, pr.URL(), p.probeURL(), p.cfg.ProbeTimeout)
	if err := store.UpdateHealth(id, res.OK, int(res.Latency.Milliseconds()), res.Err); err != nil {
		return res, err
	}
	return res, nil
}
