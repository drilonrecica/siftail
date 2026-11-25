package main

// The bounded production soak (SFT-055, ARCHITECTURE.md §36). It runs the
// production image for a fixed duration under a mixed workload with
// scheduled operational scenarios, then verifies the database against the
// harness's own record of every batch. docs/performance/0.5.0-soak.md
// describes the procedure and records the results.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	soakContainer         = "siftail-soak"
	soakRestoredContainer = "siftail-soak-restored"
	soakBaseRate          = 300
	soakSustainedRate     = 1000
	soakBurstRate         = 5000
	soakWorkers           = 32
	soakReaders           = 4
	// The size limit retention enforces during the soak; a full run writes
	// several times this, so size-triggered cleanup deletes for real.
	soakRetentionGiB = 1
)

type soakOptions struct {
	image, workdir, out string
	quick, keep         bool
	duration            time.Duration
	seed                int64
	uiPort, ingestPort  int
}

type soakEvent struct {
	Offset float64 `json:"offset_seconds"`
	Cycle  int     `json:"cycle"`
	Name   string  `json:"name"`
	OK     bool    `json:"ok"`
	Detail string  `json:"detail"`
}

type soakSample struct {
	Offset     float64 `json:"offset_seconds"`
	Lifetime   int     `json:"process_lifetime"`
	RSSMiB     float64 `json:"rss_mib"`
	HWMMiB     float64 `json:"high_water_mib"`
	Threads    int     `json:"threads"`
	FDs        int     `json:"open_fds,omitempty"`
	CPUSeconds float64 `json:"cpu_seconds"`
	Rate       float64 `json:"target_events_per_second"`
	HostLoad1  float64 `json:"host_load_1m"`
	DBBytes    int64   `json:"database_bytes,omitempty"`
	WALBytes   int64   `json:"wal_bytes,omitempty"`
	Queued     string  `json:"queued_events,omitempty"`
	Rejected   string  `json:"rejected_batches,omitempty"`
}

type soakCheckpoint struct {
	Cycle    int     `json:"cycle"`
	Lifetime int     `json:"process_lifetime"`
	Offset   float64 `json:"offset_seconds"`
	RSSMiB   float64 `json:"rss_mib"`
	Threads  int     `json:"threads"`
	FDs      int     `json:"open_fds"`
}

type soakSummary struct {
	Started      time.Time          `json:"started"`
	Finished     time.Time          `json:"finished"`
	Environment  map[string]string  `json:"environment"`
	Plan         map[string]any     `json:"plan"`
	Timeline     []soakEvent        `json:"timeline"`
	Checkpoints  []soakCheckpoint   `json:"checkpoints"`
	Samples      []soakSample       `json:"samples"`
	Minutes      []minuteSummary    `json:"minutes"`
	Ingestion    map[string]any     `json:"ingestion"`
	Readers      map[string]any     `json:"readers"`
	Live         map[string]any     `json:"live"`
	Final        map[string]any     `json:"final"`
	Verification *soakVerification  `json:"verification,omitempty"`
	Startup      map[string]float64 `json:"startup_seconds"`
	Failures     []string           `json:"failures"`
}

type window struct {
	from, to time.Time
	name     string
}

type soakRun struct {
	*benchRun
	opts     soakOptions
	started  time.Time
	cycleLen time.Duration
	scale    float64
	rng      *rand.Rand

	pid      atomic.Int64
	lifetime atomic.Int64
	probeMu  sync.Mutex
	probes   []time.Time // when each acknowledged token-rotation probe was sent
	in       *soakIngester

	mu       sync.Mutex
	soak     soakSummary
	windows  []window
	adminMu  sync.Mutex
	adminB   *browser
	liveMu   sync.Mutex
	liveStop []context.CancelFunc

	readerReads    []time.Duration
	readerExpected int
	readerErrors   []string
	readerSlow     []string
	relogins       atomic.Int64
	liveConns      atomic.Int64
	liveFrames     atomic.Int64
	liveServerEnd  atomic.Int64
	liveIdle       []string
	liveFailures   []string
	liveExpected   int
	slowClients    []map[string]any
}

func runSoak(opts soakOptions) int {
	if opts.quick {
		opts.duration = 12 * time.Minute
	}
	cycle := 30 * time.Minute
	if opts.quick {
		cycle = 4 * time.Minute
	}
	if opts.duration < cycle {
		log.Printf("soak: duration %s is shorter than one %s cycle", opts.duration, cycle)
		return 2
	}
	bench := &benchRun{
		name: soakContainer, image: opts.image, workdir: opts.workdir, quick: opts.quick,
		ui:     fmt.Sprintf("http://127.0.0.1:%d", opts.uiPort),
		ingest: fmt.Sprintf("http://127.0.0.1:%d/api/v1/ingest", opts.ingestPort),
		uiPort: opts.uiPort, ingestPort: opts.ingestPort,
		summary: summary{Environment: map[string]string{}, Startup: map[string]float64{}},
	}
	r := &soakRun{
		benchRun: bench, opts: opts, cycleLen: cycle,
		scale: float64(cycle) / float64(30*time.Minute),
		rng:   rand.New(rand.NewSource(opts.seed)),
		soak: soakSummary{
			Started: time.Now().UTC(), Plan: map[string]any{}, Ingestion: map[string]any{},
			Readers: map[string]any{}, Live: map[string]any{}, Final: map[string]any{},
			Startup: map[string]float64{}, Failures: []string{},
		},
	}
	err := r.execute()
	r.soak.Finished = time.Now().UTC()
	if err != nil {
		r.fail("aborted: %v", err)
		log.Printf("FAILED: %v", err)
	}
	r.soak.Environment = bench.summary.Environment
	for key, value := range bench.summary.Startup {
		r.soak.Startup[key] = value
	}
	encoded, _ := json.MarshalIndent(r.soak, "", "  ")
	if writeErr := os.WriteFile(opts.out, append(encoded, '\n'), 0o644); writeErr != nil {
		log.Print(writeErr)
		return 1
	}
	if !opts.keep {
		r.cleanupSoak()
	}
	if len(r.soak.Failures) > 0 {
		log.Printf("soak FAILED with %d failures:", len(r.soak.Failures))
		for _, failure := range r.soak.Failures {
			log.Printf("  - %s", failure)
		}
		return 1
	}
	log.Printf("soak passed")
	return 0
}

// scaled shortens a full-size duration for -quick, never below floor.
func (r *soakRun) scaled(d, floor time.Duration) time.Duration {
	return max(time.Duration(float64(d)*r.scale), floor)
}

func (r *soakRun) offset() float64 { return time.Since(r.started).Seconds() }

func (r *soakRun) fail(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.soak.Failures = append(r.soak.Failures, fmt.Sprintf(format, args...))
}

func (r *soakRun) event(cycle int, name string, ok bool, format string, args ...any) {
	detail := fmt.Sprintf(format, args...)
	r.mu.Lock()
	r.soak.Timeline = append(r.soak.Timeline, soakEvent{Offset: r.offset(), Cycle: cycle, Name: name, OK: ok, Detail: detail})
	r.mu.Unlock()
	mark := "ok"
	if !ok {
		mark = "FAILED"
		r.fail("cycle %d %s: %s", cycle, name, detail)
	}
	log.Printf("[%7.0fs] cycle %d %-20s %-6s %s", r.offset(), cycle, name, mark, detail)
}

// disrupt records a window in which reader and Live errors are expected.
func (r *soakRun) disrupt(name string) func() {
	from := time.Now()
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.windows = append(r.windows, window{from: from, to: time.Now(), name: name})
	}
}

// disrupted reports whether t falls inside a disruption window, an open
// one, or the minute after one ends.
func (r *soakRun) disrupted(t time.Time, open *atomic.Bool) bool {
	if open != nil && open.Load() {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, w := range r.windows {
		if !t.Before(w.from.Add(-5*time.Second)) && !t.After(w.to.Add(time.Minute)) {
			return true
		}
	}
	return false
}

var disruptionOpen atomic.Bool

// signIn signs in, waiting out the sign-in rate limit.
func signIn(ctx context.Context, base string) (*browser, error) {
	for deadline := time.Now().Add(3 * time.Minute); ; {
		b, err := newBrowser(base, adminUser, adminPassword)
		if err == nil || !strings.Contains(err.Error(), "HTTP 429") || time.Now().After(deadline) {
			return b, err
		}
		sleepCtx(ctx, 5*time.Second)
	}
}

// admin returns the one administrator session that the History readers,
// the Live streams, the slow client, and the status sampler share, as
// browser tabs share a cookie. It signs in again after a revocation.
func (r *soakRun) admin() *browser {
	b, _ := r.session(context.Background())
	return b
}

func (r *soakRun) session(ctx context.Context) (*browser, error) {
	r.adminMu.Lock()
	defer r.adminMu.Unlock()
	if r.adminB != nil {
		return r.adminB, nil
	}
	b, err := signIn(ctx, r.ui)
	if err != nil {
		return nil, err
	}
	r.adminB = b
	return b, nil
}

// ended drops a session the server no longer accepts; the next caller
// signs in again, once, for every tab.
func (r *soakRun) ended(old *browser) {
	r.adminMu.Lock()
	defer r.adminMu.Unlock()
	if r.adminB == old && old != nil {
		r.adminB = nil
		r.relogins.Add(1)
	}
}

func (r *soakRun) relogin() (*browser, error) {
	r.ended(r.admin())
	return r.session(context.Background())
}

func (r *soakRun) statusFields(ctx context.Context) (map[string]string, error) {
	b, err := r.session(ctx)
	if err != nil {
		return nil, err
	}
	fields, err := b.status(ctx)
	if err != nil && sessionEnded(err) {
		r.ended(b)
		if b, err = r.session(ctx); err != nil {
			return nil, err
		}
		fields, err = b.status(ctx)
	}
	return fields, err
}

func sessionEnded(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "HTTP 303") || strings.Contains(err.Error(), "HTTP 401"))
}

func (r *soakRun) execute() error {
	ctx := context.Background()
	r.recordEnvironment()
	r.dataDir = filepath.Join(r.workdir, "data")
	r.cleanupSoak()
	if err := os.MkdirAll(r.dataDir, 0o750); err != nil {
		return err
	}
	if _, err := podman("unshare", "chown", "65532:65532", r.dataDir); err != nil {
		return err
	}
	tokens, ids, err := r.setupSoak()
	if err != nil {
		return err
	}
	r.started = time.Now()
	if err := r.startSoak(ctx, "first_start"); err != nil {
		return err
	}
	if _, err := r.session(ctx); err != nil {
		return err
	}
	retentionGiB := soakRetentionGiB
	if err := r.admin().saveRetention(ctx, 14, retentionGiB); err != nil {
		return err
	}
	cycles := int(r.opts.duration / r.cycleLen)
	r.soak.Plan = map[string]any{
		"duration_seconds": r.opts.duration.Seconds(), "cycle_seconds": r.cycleLen.Seconds(),
		"cycles": cycles, "seed": r.opts.seed, "quick": r.opts.quick,
		"base_rate": soakBaseRate, "sustained_rate": soakSustainedRate, "burst_rate": soakBurstRate,
		"batch_events": fixtureBlock, "ingest_workers": soakWorkers, "browser_readers": soakReaders,
		"live_streams": soakReaders, "slow_live_clients": 1,
		"retention_days": 14, "retention_max_gib": retentionGiB,
		"live_reconnect_seconds": r.scaled(2*time.Minute, 20*time.Second).Seconds(),
	}

	r.in = newSoakIngester(r.ingest, newFixture(time.Now()), tokens, ids)
	r.in.started = r.started
	r.in.setRate(soakBaseRate)

	stopIngest := make(chan struct{})
	drainCtx, drainCancel := context.WithCancel(ctx)
	defer drainCancel()
	ingestDone := make(chan struct{})
	go func() {
		r.in.run(stopIngest, drainCtx, soakWorkers)
		close(ingestDone)
	}()
	readCtx, readCancel := context.WithCancel(ctx)
	defer readCancel()
	var background sync.WaitGroup
	r.startReaders(readCtx, &background)
	r.startLive(readCtx, &background)
	r.startSampler(readCtx, &background)

	for cycle := range cycles {
		if err := r.runCycle(ctx, cycle); err != nil {
			close(stopIngest)
			drainCancel()
			readCancel()
			background.Wait()
			return err
		}
	}

	// Stop offering load, let retries finish, then stop the readers.
	r.in.setRate(0)
	close(stopIngest)
	select {
	case <-ingestDone:
	case <-time.After(3 * time.Minute):
		drainCancel()
		<-ingestDone
		r.fail("ingestion retries did not finish within 3 minutes of the load stopping")
	}
	r.event(cycles, "load_stopped", true, "%d batches recorded", len(r.in.batches))
	readCancel()
	background.Wait()
	r.summarizeLoad()
	return r.finish(ctx, cycles)
}

func (r *soakRun) setupSoak() ([]string, []int64, error) {
	password := []byte(adminPassword + "\n" + adminPassword + "\n")
	if _, err := r.runCLI(password, "admin", "create", "--username", adminUser); err != nil {
		return nil, nil, err
	}
	for _, server := range fixtureServers {
		if _, err := r.runCLI(nil, "server", "create", "--name", server.name); err != nil {
			return nil, nil, err
		}
	}
	var tokens []string
	var ids []int64
	for index := range fixtureServers {
		output, err := r.runCLI(nil, "token", "create", "--server", strconv.Itoa(index+1), "--name", "soak")
		if err != nil {
			return nil, nil, err
		}
		token, id, err := parseToken(output)
		if err != nil {
			return nil, nil, err
		}
		tokens, ids = append(tokens, token), append(ids, id)
	}
	return tokens, ids, nil
}

func parseToken(output string) (string, int64, error) {
	token, id := "", int64(0)
	for _, line := range strings.Split(output, "\n") {
		if value, ok := strings.CutPrefix(line, "token (shown once): "); ok {
			token = strings.TrimSpace(value)
		}
		if _, err := fmt.Sscanf(line, "token %d created", &id); err == nil {
			continue
		}
	}
	if token == "" || id == 0 {
		return "", 0, fmt.Errorf("token create printed no token: %q", output)
	}
	return token, id, nil
}

func (r *soakRun) startSoak(ctx context.Context, label string) error {
	if err := r.start(ctx, label); err != nil {
		return err
	}
	r.pid.Store(int64(r.benchRun.pid))
	return nil
}

func (r *soakRun) restartSoak(ctx context.Context, label string) (float64, error) {
	stopped := time.Now()
	if _, err := podman("stop", "--time", "30", r.name); err != nil {
		return 0, err
	}
	stopSeconds := time.Since(stopped).Seconds()
	started := time.Now()
	if _, err := podman("start", r.name); err != nil {
		return stopSeconds, err
	}
	if err := r.awaitStart(ctx, label, started); err != nil {
		return stopSeconds, err
	}
	r.pid.Store(int64(r.benchRun.pid))
	r.lifetime.Add(1)
	return stopSeconds, nil
}

func (r *soakRun) sleepUntil(cycleStart time.Time, fullMinutes float64) {
	at := cycleStart.Add(time.Duration(fullMinutes * float64(time.Minute) * r.scale))
	if wait := time.Until(at); wait > 0 {
		time.Sleep(wait)
	}
}

// runCycle is one 30-minute block of scheduled scenarios (offsets are in
// full-size minutes and scale down for -quick). The disruptive scenario
// rotates: restart, saturation, degraded database.
func (r *soakRun) runCycle(ctx context.Context, cycle int) error {
	start := time.Now()
	log.Printf("== cycle %d", cycle)
	go r.slowClient(ctx, cycle, r.scaled(10*time.Minute, 30*time.Second))

	r.sleepUntil(start, 4)
	r.checkpoint(cycle)

	r.sleepUntil(start, 5)
	r.in.setRate(soakSustainedRate)
	r.sleepUntil(start, 10)
	r.in.setRate(soakBaseRate)
	r.event(cycle, "sustained_1000", true, "1,000 events/s for %s", r.scaled(5*time.Minute, 0))

	r.sleepUntil(start, 11)
	burst := r.scaled(30*time.Second, 4*time.Second)
	r.in.setRate(soakBurstRate)
	time.Sleep(burst)
	r.in.setRate(soakBaseRate)
	r.event(cycle, "burst_5000", true, "5,000 events/s for %s", burst)

	r.sleepUntil(start, 13)
	r.replayScenario(ctx, cycle)

	r.sleepUntil(start, 14)
	r.rotateToken(ctx, cycle)

	r.sleepUntil(start, 16)
	r.sessionScenario(ctx, cycle)

	r.sleepUntil(start, 18)
	r.sourceTransition(ctx, cycle)

	r.sleepUntil(start, 20)
	r.backupScenario(ctx, cycle, "soak-backup.sqlite", true)

	r.sleepUntil(start, 23)
	switch cycle % 3 {
	case 0:
		if err := r.restartScenario(ctx, cycle); err != nil {
			return err
		}
	case 1:
		r.saturationScenario(ctx, cycle)
	case 2:
		r.degradedScenario(ctx, cycle)
	}
	r.sleepUntil(start, 30)
	return nil
}

func (r *soakRun) checkpoint(cycle int) {
	pid := int(r.pid.Load())
	sample, _ := readProcess(pid)
	point := soakCheckpoint{
		Cycle: cycle, Lifetime: int(r.lifetime.Load()), Offset: r.offset(),
		RSSMiB: mib(sample.rssKiB), Threads: threads(pid), FDs: openFDs(pid),
	}
	r.mu.Lock()
	r.soak.Checkpoints = append(r.soak.Checkpoints, point)
	r.mu.Unlock()
	log.Printf("[%7.0fs] cycle %d checkpoint: rss %.1f MiB, %d threads, %d fds (process lifetime %d)",
		r.offset(), cycle, point.RSSMiB, point.Threads, point.FDs, point.Lifetime)
}

// hostLoad is the one-minute load average: the soak shares its host, and
// latency means little without it.
func hostLoad() float64 {
	text, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return -1
	}
	load, _ := strconv.ParseFloat(strings.Fields(string(text))[0], 64)
	return load
}

func threads(pid int) int {
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(status), "\n") {
		if value, ok := strings.CutPrefix(line, "Threads:"); ok {
			n, _ := strconv.Atoi(strings.TrimSpace(value))
			return n
		}
	}
	return -1
}

// openFDs counts the process's descriptors. The process belongs to the
// container's user namespace, so the listing runs inside it.
func openFDs(pid int) int {
	output, err := podman("unshare", "ls", fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		return -1
	}
	return len(strings.Fields(output))
}

func (r *soakRun) startSampler(ctx context.Context, wg *sync.WaitGroup) {
	interval := 15 * time.Second
	if r.opts.quick {
		interval = 3 * time.Second
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for turn := 0; ; turn++ {
			pid := int(r.pid.Load())
			sample := soakSample{Offset: r.offset(), Lifetime: int(r.lifetime.Load()), Rate: r.in.rate(), Threads: threads(pid), HostLoad1: hostLoad()}
			if process, err := readProcess(pid); err == nil {
				sample.RSSMiB, sample.HWMMiB, sample.CPUSeconds = mib(process.rssKiB), mib(process.hwmKiB), process.cpu
			}
			if turn%4 == 0 {
				sample.FDs = openFDs(pid)
				disk := diskUsage(r.dataDir)
				sample.DBBytes, sample.WALBytes = disk.DatabaseBytes, disk.WALBytes
				statusCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
				if fields, err := r.statusFields(statusCtx); err == nil {
					sample.Queued, sample.Rejected = fields["Queued events"], fields["Rejected batches"]
				}
				cancel()
			}
			r.mu.Lock()
			r.soak.Samples = append(r.soak.Samples, sample)
			r.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// startReaders signs in four browser sessions that each read a History
// page every second, rotating through primary filters.
func (r *soakRun) startReaders(ctx context.Context, wg *sync.WaitGroup) {
	scopes := []url.Values{nil, largestService, {"levels": {"error,fatal"}}, partialApp, {"contains": {fixtureNeedle}}}
	for index := range soakReaders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for turn := 0; ctx.Err() == nil; turn++ {
				b, err := r.session(ctx)
				if err != nil {
					r.readerError(time.Now(), "sign-in: "+err.Error())
					sleepCtx(ctx, 2*time.Second)
					continue
				}
				now := time.Now()
				values := rangeValues(now.Add(-time.Hour), now.Add(time.Minute), scopes[(index+turn)%len(scopes)])
				p, err := b.history(ctx, "/logs/rows?"+values.Encode())
				switch {
				case ctx.Err() != nil:
				case err != nil && (p.status == http.StatusSeeOther || p.status == http.StatusUnauthorized):
					r.ended(b)
				case err != nil:
					r.readerError(now, err.Error())
				default:
					r.mu.Lock()
					r.readerReads = append(r.readerReads, p.elapsed)
					r.mu.Unlock()
					if p.elapsed > 10*time.Second && !r.disrupted(now, &disruptionOpen) {
						r.mu.Lock()
						r.readerSlow = append(r.readerSlow, fmt.Sprintf("%.0fs: %.1f s", r.offset(), p.elapsed.Seconds()))
						r.mu.Unlock()
					}
				}
				sleepCtx(ctx, time.Second)
			}
		}()
	}
}

func (r *soakRun) readerError(at time.Time, message string) {
	expected := r.disrupted(at, &disruptionOpen)
	r.mu.Lock()
	defer r.mu.Unlock()
	if expected {
		r.readerExpected++
		return
	}
	if len(r.readerErrors) < 100 {
		r.readerErrors = append(r.readerErrors, fmt.Sprintf("%.0fs: %s", r.offset(), message))
	}
}

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// startLive holds one Live stream per reader session and reconnects them
// all on a fixed interval, as a browser tab being reloaded would.
func (r *soakRun) startLive(ctx context.Context, wg *sync.WaitGroup) {
	reconnect := r.scaled(2*time.Minute, 20*time.Second)
	for range soakReaders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				b, err := r.session(ctx)
				if err != nil {
					r.liveFailure(time.Now(), "sign-in: "+err.Error())
					sleepCtx(ctx, 2*time.Second)
					continue
				}
				connCtx, cancel := context.WithCancel(ctx)
				r.liveMu.Lock()
				r.liveStop = append(r.liveStop, cancel)
				r.liveMu.Unlock()
				opened := time.Now()
				frames, err := b.live(connCtx)
				clientClosed := connCtx.Err() != nil
				cancel()
				r.liveConns.Add(1)
				r.liveFrames.Add(frames)
				switch {
				case ctx.Err() != nil:
				case sessionEnded(err):
					r.ended(b)
				case err != nil:
					r.liveFailure(opened, err.Error())
					sleepCtx(ctx, time.Second)
				case !clientClosed:
					// Server ended the stream: a restart, or truncation
					// after a burst; the client reconnects.
					r.liveServerEnd.Add(1)
					sleepCtx(ctx, time.Second)
				case frames == 0 && time.Since(opened) > 30*time.Second && !r.disrupted(opened, &disruptionOpen) && !r.disrupted(time.Now(), nil):
					r.mu.Lock()
					r.liveIdle = append(r.liveIdle, fmt.Sprintf("%.0fs: no frames in %.0f s", r.offset(), time.Since(opened).Seconds()))
					r.mu.Unlock()
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(reconnect)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.liveMu.Lock()
				for _, cancel := range r.liveStop {
					cancel()
				}
				r.liveStop = nil
				r.liveMu.Unlock()
			}
		}
	}()
}

func (r *soakRun) liveFailure(at time.Time, message string) {
	expected := r.disrupted(at, &disruptionOpen) || r.disrupted(time.Now(), &disruptionOpen)
	r.mu.Lock()
	defer r.mu.Unlock()
	if expected {
		r.liveExpected++
		return
	}
	if len(r.liveFailures) < 100 {
		r.liveFailures = append(r.liveFailures, fmt.Sprintf("%.0fs: %s", r.offset(), message))
	}
}

// slowClient opens a Live stream and does not read it, then reports
// whether Siftail closed it instead of blocking.
func (r *soakRun) slowClient(ctx context.Context, cycle int, hold time.Duration) {
	result := map[string]any{"cycle": cycle, "hold_seconds": hold.Seconds()}
	defer func() {
		r.mu.Lock()
		r.slowClients = append(r.slowClients, result)
		r.mu.Unlock()
	}()
	b, err := r.session(ctx)
	if err != nil {
		result["error"] = err.Error()
		return
	}
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	request, _ := http.NewRequestWithContext(connCtx, http.MethodGet, b.base+"/logs/live/stream", nil)
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Origin", b.base)
	client := *b.client
	client.Timeout = 0
	response, err := client.Do(request)
	if err != nil {
		result["error"] = err.Error()
		return
	}
	defer response.Body.Close()
	result["status"] = response.StatusCode
	time.Sleep(hold)
	done := make(chan struct{})
	var read int64
	var readErr error
	go func() {
		read, readErr = io.Copy(io.Discard, response.Body)
		close(done)
	}()
	select {
	case <-done:
		result["closed_by_server"] = true
		if readErr != nil {
			result["read_error"] = readErr.Error()
		}
	case <-time.After(10 * time.Second):
		result["closed_by_server"] = false
		cancel()
		<-done
	}
	result["bytes_buffered"] = read
}

func (r *soakRun) replayScenario(ctx context.Context, cycle int) {
	batches := r.in.recentAcked(21, 10*time.Minute)
	if len(batches) < 2 {
		r.event(cycle, "replay", false, "only %d recent acknowledged batches", len(batches))
		return
	}
	conflict, identical := batches[0], batches[1:]
	statuses := r.in.replay(ctx, identical, false)
	before, err := r.statusFields(ctx)
	if err != nil {
		r.event(cycle, "replay", false, "status: %v", err)
		return
	}
	conflictStatus := r.in.replay(ctx, []*soakBatch{conflict}, true)
	after, err := r.statusFields(ctx)
	if err != nil {
		r.event(cycle, "replay", false, "status: %v", err)
		return
	}
	dropped := parseCount(after["Conflicting duplicates dropped"]) - parseCount(before["Conflicting duplicates dropped"])
	ok := statuses["204"] == len(identical) && conflictStatus["204"] == 1 && dropped == conflict.events()
	r.event(cycle, "replay", ok, "%d identical batches resent %v; 1 conflicting batch %v; conflicting duplicates dropped +%d (want %d)",
		len(identical), statuses, conflictStatus, dropped, conflict.events())
}

func parseCount(text string) int64 {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, strings.SplitN(text, " ", 2)[0])
	n, _ := strconv.ParseInt(digits, 10, 64)
	return n
}

func (r *soakRun) rotateToken(ctx context.Context, cycle int) {
	server := cycle % len(fixtureServers)
	oldToken, oldID := r.in.token(server)
	output, err := podman("exec", r.name, "/siftail", "token", "create", "--server", strconv.Itoa(server+1), "--name", fmt.Sprintf("soak-rotation-%d", cycle))
	if err != nil {
		r.event(cycle, "token_rotation", false, "create: %v", err)
		return
	}
	token, id, err := parseToken(output)
	if err != nil {
		r.event(cycle, "token_rotation", false, "%v", err)
		return
	}
	r.in.setToken(server, token, id)
	// In-flight requests still carry the old token; revoke it while they
	// may be on the wire, as an operator rotating Fluent Bit would.
	time.Sleep(r.scaled(5*time.Second, time.Second))
	if _, err := podman("exec", r.name, "/siftail", "token", "revoke", "--id", strconv.FormatInt(oldID, 10)); err != nil {
		r.event(cycle, "token_rotation", false, "revoke: %v", err)
		return
	}
	// One event with its own ID, outside the soak sequence; the offline
	// verification counts these as the only non-soak events.
	probe, _ := json.Marshal(map[string]any{
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano), "project": "soak", "environment": "probe",
		"application": "token-rotation", "service": "probe", "stream": "stdout",
		"log": "token rotation probe", "source_event_id": fmt.Sprintf("probe-rotation-%d", cycle),
	})
	probe = append(probe, '\n')
	old, _ := r.in.post(ctx, oldToken, probe)
	sent := time.Now()
	fresh, _ := r.in.post(ctx, token, probe)
	if fresh == "204" {
		r.probeMu.Lock()
		r.probes = append(r.probes, sent)
		r.probeMu.Unlock()
	}
	r.event(cycle, "token_rotation", old == "401" && fresh == "204",
		"Server %d: token %d replaced by %d; revoked token answers %s, new token %s", server+1, oldID, id, old, fresh)
}

func (r *soakRun) sessionScenario(ctx context.Context, cycle int) {
	// A second browser signs in; revoke-all must end it and the shared
	// session, sign-out only it.
	b, err := signIn(ctx, r.ui)
	if err != nil {
		r.event(cycle, "session_change", false, "sign-in: %v", err)
		return
	}
	if cycle%2 == 0 {
		previous := r.admin()
		output, err := podman("exec", r.name, "/siftail", "sessions", "revoke-all")
		if err != nil {
			r.event(cycle, "session_change", false, "revoke-all: %v", err)
			return
		}
		status, _, _, _ := b.do(ctx, http.MethodGet, "/status", nil)
		shared, _, _, _ := previous.do(ctx, http.MethodGet, "/status", nil)
		r.ended(previous)
		fresh, err := r.session(ctx)
		ok := status == http.StatusSeeOther && shared == http.StatusSeeOther && err == nil
		if err == nil {
			_, err = fresh.status(ctx)
			ok = ok && err == nil
		}
		r.event(cycle, "session_change", ok, "sessions revoke-all (%s): revoked sessions answer %d and %d; new sign-in error %v",
			strings.TrimSpace(output), status, shared, err)
		return
	}
	status, _, _, err := b.do(ctx, http.MethodPost, "/session/logout", url.Values{"csrf_token": {b.csrf}})
	if err != nil {
		r.event(cycle, "session_change", false, "sign-out: %v", err)
		return
	}
	after, _, _, _ := b.do(ctx, http.MethodGet, "/status", nil)
	shared, _, _, _ := r.admin().do(ctx, http.MethodGet, "/status", nil)
	r.event(cycle, "session_change", (status == http.StatusSeeOther || status == http.StatusOK) && after == http.StatusSeeOther && shared == http.StatusOK,
		"sign-out answered %d; the signed-out cookie then answers %d, the other session %d", status, after, shared)
}

func (r *soakRun) sourceTransition(ctx context.Context, cycle int) {
	gen := r.in.gen.Add(1)
	b := r.in.sendRelease(ctx, cycle+1)
	r.event(cycle, "source_transition", !b.Acked.IsZero(),
		"containers redeployed as generation %d; new source storefront/release-%d acknowledged=%t", gen+1, cycle+1, !b.Acked.IsZero())
}

func (r *soakRun) backupScenario(ctx context.Context, cycle int, name string, remove bool) bool {
	path := "/data/" + name
	started := time.Now()
	output, err := podman("exec", r.name, "/siftail", "backup", "--output", path)
	backupSeconds := time.Since(started).Seconds()
	if err != nil {
		r.event(cycle, "backup", false, "backup: %v", err)
		return false
	}
	started = time.Now()
	verify, err := podman("exec", r.name, "/siftail", "backup", "verify", path)
	verifySeconds := time.Since(started).Seconds()
	size, _ := podman("unshare", "stat", "-c", "%s", filepath.Join(r.dataDir, name))
	if remove {
		_, _ = podman("unshare", "rm", "-f", filepath.Join(r.dataDir, name))
	}
	r.event(cycle, "backup", err == nil, "online backup %.1f s (%s bytes), verify %.1f s: %s",
		backupSeconds, strings.TrimSpace(size), verifySeconds, lastLine(verify+" "+output))
	return err == nil
}

func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func (r *soakRun) restartScenario(ctx context.Context, cycle int) error {
	disruptionOpen.Store(true)
	end := r.disrupt("restart")
	stopSeconds, err := r.restartSoak(ctx, fmt.Sprintf("restart_cycle_%d", cycle))
	disruptionOpen.Store(false)
	end()
	if err != nil {
		r.event(cycle, "graceful_restart", false, "%v", err)
		return err
	}
	// Sessions survive a restart; the shared one keeps working.
	fields, _ := r.statusFields(ctx)
	r.event(cycle, "graceful_restart", stopSeconds < 30,
		"graceful stop under load %.1f s, ready %.1f s after start; startup retention cleanup: %s",
		stopSeconds, r.benchRun.summary.Startup[fmt.Sprintf("restart_cycle_%d", cycle)], fields["Cleanup result"])
	return nil
}

func (r *soakRun) saturationScenario(ctx context.Context, cycle int) {
	disruptionOpen.Store(true)
	end := r.disrupt("saturation")
	floodFor := r.scaled(30*time.Second, 10*time.Second)
	floodCtx, cancel := context.WithTimeout(ctx, floodFor)
	sent, acked, rejected := r.in.flood(floodCtx, 64)
	cancel()
	stopped := time.Now()
	readyCtx, readyCancel := context.WithTimeout(ctx, 2*time.Minute)
	_, err := waitReady(readyCtx, r.ui)
	readyCancel()
	recovery := time.Since(stopped)
	disruptionOpen.Store(false)
	end()
	pid := int(r.pid.Load())
	sample, _ := readProcess(pid)
	fields, _ := r.statusFields(ctx)
	r.event(cycle, "queue_saturation", err == nil && rejected > 0,
		"%s flood of %d requests ×1,000 events without retry: %d acknowledged, %d rejected; ready %.1f s after; rss %.1f MiB (high water %.1f); status rejected batches %s",
		floodFor, sent, acked, rejected, recovery.Seconds(), mib(sample.rssKiB), mib(sample.hwmKiB), fields["Rejected batches"])
}

// degradedScenario holds an external SQLite write lock on the live
// database, so Siftail's commits fail with SQLITE_BUSY and it enters its
// degraded mode, then releases it and times the recovery probe.
func (r *soakRun) degradedScenario(ctx context.Context, cycle int) {
	hold := r.scaled(60*time.Second, 20*time.Second)
	disruptionOpen.Store(true)
	end := r.disrupt("degraded")
	defer func() {
		disruptionOpen.Store(false)
		end()
	}()
	self, err := os.Executable()
	if err != nil {
		r.event(cycle, "degraded_recovery", false, "%v", err)
		return
	}
	command := exec.Command("podman", "unshare", self, "-hold-write-lock", filepath.Join(r.dataDir, "siftail.db"), "-hold-for", hold.String())
	stdout, _ := command.StdoutPipe()
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		r.event(cycle, "degraded_recovery", false, "lock holder: %v", err)
		return
	}
	locked := make(chan bool, 1)
	go func() {
		buffer := make([]byte, 64)
		n, _ := stdout.Read(buffer)
		locked <- strings.Contains(string(buffer[:n]), "locked")
		_, _ = io.Copy(io.Discard, stdout)
	}()
	if !<-locked {
		_ = command.Wait()
		r.event(cycle, "degraded_recovery", false, "lock holder did not take the write lock")
		return
	}
	lockedAt := time.Now()
	client := newHTTPClient(3 * time.Second)
	var unreadyAt, readyAt time.Time
	var degradedStatus string
	released := make(chan error, 1)
	go func() { released <- command.Wait() }()
	var releasedAt time.Time
	for deadline := time.Now().Add(hold + 2*time.Minute); time.Now().Before(deadline); {
		select {
		case err := <-released:
			releasedAt = time.Now()
			if err != nil {
				log.Printf("lock holder: %v", err)
			}
			released = nil
		default:
		}
		ready := false
		if response, err := client.Get(r.ui + "/health/ready"); err == nil {
			ready = response.StatusCode == http.StatusOK
			_ = response.Body.Close()
		}
		if !ready && unreadyAt.IsZero() {
			unreadyAt = time.Now()
			if fields, err := r.statusFields(ctx); err == nil {
				degradedStatus = fields["Last database error"]
			}
		}
		if ready && !unreadyAt.IsZero() && !releasedAt.IsZero() {
			readyAt = time.Now()
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if released != nil {
		<-released
		releasedAt = time.Now()
	}
	ok := !unreadyAt.IsZero() && !readyAt.IsZero() && readyAt.Sub(releasedAt) < 30*time.Second
	r.event(cycle, "degraded_recovery", ok,
		"external write lock held %s: not ready %.1f s after locking (status: %q); ready again %.1f s after release",
		hold, unreadyAt.Sub(lockedAt).Seconds(), degradedStatus, readyAt.Sub(releasedAt).Seconds())
}

func (r *soakRun) summarizeLoad() {
	in := r.in
	in.mu.Lock()
	var acked, ackedEvents, retried, ambiguous, rejected, flood int64
	statuses := map[string]int{}
	for _, b := range in.batches {
		if !b.Acked.IsZero() {
			acked++
			ackedEvents += b.events()
		}
		if b.Attempts > 1 {
			retried++
		}
		if b.Ambiguous {
			ambiguous++
		}
		if b.Rejected {
			rejected++
		}
		if b.Flood {
			flood++
		}
		for status, count := range b.Statuses {
			statuses[status] += count
		}
	}
	r.soak.Ingestion = map[string]any{
		"batches": len(in.batches), "batches_acknowledged": acked, "events_acknowledged": ackedEvents,
		"batches_retried": retried, "batches_ambiguous": ambiguous, "flood_batches": flood,
		"flood_batches_rejected": rejected, "attempt_status_counts": statuses,
		"unexpected": in.unexpect,
	}
	unexpected := len(in.unexpect)
	in.mu.Unlock()
	r.soak.Minutes = in.minuteSummaries()
	r.soak.Ingestion["commit_latency_ms_all"] = in.allLatencies(0, 1<<62)
	r.soak.Ingestion["commit_latency_ms_outside_disruptions"] = in.latenciesWhere(func(minute time.Time) bool {
		return !r.disrupted(minute, nil) && !r.disrupted(minute.Add(time.Minute), nil)
	})
	if unexpected > 0 {
		r.fail("%d unexpected ingestion outcomes (see ingestion.unexpected)", unexpected)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.soak.Readers = map[string]any{
		"sessions": soakReaders, "history_reads_ms": summarize(r.readerReads),
		"errors_in_disruption_windows": r.readerExpected, "errors_outside_disruptions": r.readerErrors,
		"reads_over_10s_outside_disruptions": r.readerSlow, "session_relogins": r.relogins.Load(),
	}
	r.soak.Live = map[string]any{
		"streams": soakReaders, "connections": r.liveConns.Load(), "frames": r.liveFrames.Load(),
		"ended_by_server": r.liveServerEnd.Load(), "failures_in_disruption_windows": r.liveExpected,
		"failures_outside_disruptions": r.liveFailures, "idle_connections": r.liveIdle,
		"slow_clients": r.slowClients,
	}
	if len(r.readerErrors) > 0 {
		r.soak.Failures = append(r.soak.Failures, fmt.Sprintf("%d History read errors outside disruption windows", len(r.readerErrors)))
	}
	if len(r.readerSlow) > 0 {
		r.soak.Failures = append(r.soak.Failures, fmt.Sprintf("%d History reads over 10 s outside disruption windows", len(r.readerSlow)))
	}
	if len(r.liveFailures) > 0 {
		r.soak.Failures = append(r.soak.Failures, fmt.Sprintf("%d Live connection failures outside disruption windows", len(r.liveFailures)))
	}
	if len(r.liveIdle) > 0 {
		r.soak.Failures = append(r.soak.Failures, fmt.Sprintf("%d Live connections received no frames under load", len(r.liveIdle)))
	}
	for _, slow := range r.slowClients {
		if slow["status"] != http.StatusOK {
			r.soak.Failures = append(r.soak.Failures, fmt.Sprintf("slow Live client in cycle %v could not connect: %v", slow["cycle"], slow["error"]))
		}
	}
}

// finish runs the post-soak checks: idle memory, Live capacity, critical
// queries, online check, final backup and offline verification, graceful
// stop with a full check, and a restore drill.
func (r *soakRun) finish(ctx context.Context, cycle int) error {
	log.Printf("== final checks")
	time.Sleep(r.scaled(30*time.Second, 5*time.Second))
	idle := startSampler(int(r.pid.Load()), time.Second)
	time.Sleep(r.scaled(60*time.Second, 10*time.Second))
	r.soak.Final["idle_after_soak"] = idle.finish()
	r.checkpoint(cycle)
	r.analyseLeaks()

	r.liveCapacity(ctx, cycle)
	r.criticalQueries(ctx, cycle)

	output, err := podman("exec", r.name, "/siftail", "database", "check")
	r.event(cycle, "online_check", err == nil, "%s", lastLine(output))
	fields, _ := r.statusFields(ctx)
	r.soak.Final["status"] = fields
	r.soak.Final["disk_before_stop"] = diskUsage(r.dataDir)

	online := r.backupScenario(ctx, cycle, "soak-final.sqlite", false)
	stopped := time.Now()
	if _, err := podman("stop", "--time", "30", r.name); err != nil {
		return err
	}
	r.event(cycle, "final_stop", time.Since(stopped) < 30*time.Second, "graceful stop %.1f s", time.Since(stopped).Seconds())
	output, err = r.runCLI(nil, "database", "check", "--full")
	r.event(cycle, "offline_full_check", err == nil, "%s", lastLine(output))
	if !online {
		// The online failure is already recorded; a stopped-server backup
		// still lets the verification run.
		_, _ = podman("unshare", "rm", "-f", filepath.Join(r.dataDir, "soak-final.sqlite"))
		output, err = r.runCLI(nil, "backup", "--output", "/data/soak-final.sqlite")
		r.event(cycle, "offline_backup", err == nil, "stopped-server backup: %s", lastLine(output))
		if err != nil {
			return errors.New("no final backup to verify")
		}
	}

	copyDir := filepath.Join(r.workdir, "verify")
	_, _ = podman("unshare", "rm", "-rf", copyDir)
	if err := os.MkdirAll(copyDir, 0o700); err != nil {
		return err
	}
	analysed := filepath.Join(copyDir, "final.sqlite")
	if _, err := podman("unshare", "cp", filepath.Join(r.dataDir, "soak-final.sqlite"), analysed); err != nil {
		return err
	}
	verification, err := verifySoak(analysed, r.in)
	if err != nil {
		return fmt.Errorf("offline verification: %w", err)
	}
	r.soak.Verification = verification
	// Size retention deletes probes with everything else received before
	// the cutoff; every later probe must be stored, and nothing else.
	r.probeMu.Lock()
	total, kept := int64(len(r.probes)), int64(0)
	for _, sent := range r.probes {
		if sent.UnixMicro() > verification.RetentionCutoffUS {
			kept++
		}
	}
	r.probeMu.Unlock()
	if verification.OtherEvents < kept || verification.OtherEvents > total {
		verification.Problems = append(verification.Problems, fmt.Sprintf("%d non-soak events stored; %d probes acknowledged, %d of them after the retention cutoff", verification.OtherEvents, total, kept))
	}
	for _, problem := range verification.Problems {
		r.fail("verification: %s", problem)
	}
	r.event(cycle, "offline_verification", len(verification.Problems) == 0, "%s", verification.String())
	if !r.opts.quick && verification.RetentionDeletedBatches == 0 {
		r.fail("size retention never deleted an acknowledged batch; the retention scenario did not run")
	}
	r.restoreDrill(ctx, cycle)
	return nil
}

// analyseLeaks compares the base-rate checkpoints within each process
// lifetime: memory, threads, and descriptors must not keep growing.
func (r *soakRun) analyseLeaks() {
	r.mu.Lock()
	points := append([]soakCheckpoint(nil), r.soak.Checkpoints...)
	r.mu.Unlock()
	byLifetime := map[int][]soakCheckpoint{}
	for _, point := range points {
		byLifetime[point.Lifetime] = append(byLifetime[point.Lifetime], point)
	}
	var lifetimes []int
	for lifetime := range byLifetime {
		lifetimes = append(lifetimes, lifetime)
	}
	sort.Ints(lifetimes)
	var report []string
	for _, lifetime := range lifetimes {
		series := byLifetime[lifetime]
		if len(series) < 2 {
			continue
		}
		first, last := series[0], series[len(series)-1]
		growth := fmt.Sprintf("lifetime %d over %.0f s: rss %+.1f MiB, threads %+d, fds %+d",
			lifetime, last.Offset-first.Offset, last.RSSMiB-first.RSSMiB, last.Threads-first.Threads, last.FDs-first.FDs)
		report = append(report, growth)
		// The thresholds are the normal-memory regression threshold
		// (ARCHITECTURE.md §33.4) and small absolute counts.
		if last.RSSMiB-first.RSSMiB > 25 || last.Threads-first.Threads > 8 || (first.FDs > 0 && last.FDs-first.FDs > 16) {
			r.fail("unexplained growth: %s", growth)
		}
	}
	r.soak.Final["growth"] = report
	log.Printf("growth: %v", report)
}

// liveCapacity opens the 16 Live subscriptions the process allows; any
// slot a reconnect or slow client leaked would refuse one of them.
func (r *soakRun) liveCapacity(ctx context.Context, cycle int) {
	b := r.admin()
	probeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	statuses := map[int]int{}
	var responses []*http.Response
	for range 17 {
		request, _ := http.NewRequestWithContext(probeCtx, http.MethodGet, b.base+"/logs/live/stream", nil)
		request.Header.Set("Accept", "text/event-stream")
		request.Header.Set("Origin", b.base)
		client := *b.client
		client.Timeout = 0
		response, err := client.Do(request)
		if err != nil {
			statuses[0]++
			continue
		}
		statuses[response.StatusCode]++
		responses = append(responses, response)
	}
	for _, response := range responses {
		_ = response.Body.Close()
	}
	r.event(cycle, "live_capacity", statuses[http.StatusOK] == 16,
		"17 concurrent Live streams after the soak: %v (16 allowed)", statuses)
}

// criticalQueries checks History correctness after the soak on the live
// server: newest page, cursor walk without duplicates, filters, an exact
// request ID, detail, and export.
func (r *soakRun) criticalQueries(ctx context.Context, cycle int) {
	b := r.admin()
	now := time.Now()
	window := rangeValues(now.Add(-time.Hour), now.Add(time.Minute), nil)
	var results []string
	ok := true
	check := func(name string, pass bool, format string, args ...any) {
		results = append(results, fmt.Sprintf("%s: %s", name, fmt.Sprintf(format, args...)))
		ok = ok && pass
	}
	first, err := b.history(ctx, "/logs/rows?"+window.Encode())
	if err != nil {
		r.event(cycle, "critical_queries", false, "newest page: %v", err)
		return
	}
	check("newest", first.rows == 200, "%d rows in %.0f ms", first.rows, ms(first.elapsed))
	seen := map[string]bool{}
	duplicates, pages, rows := 0, 1, first.rows
	for _, id := range first.ids {
		seen[id] = true
	}
	for next := first.nextURL; next != "" && pages < 26; pages++ {
		p, err := b.history(ctx, next)
		if err != nil {
			check("cursor", false, "%v", err)
			break
		}
		for _, id := range p.ids {
			if seen[id] {
				duplicates++
			}
			seen[id] = true
		}
		rows += p.rows
		next = p.nextURL
	}
	check("cursor", duplicates == 0 && len(seen) == rows, "%d pages, %d rows, %d duplicates", pages, rows, duplicates)
	for _, filter := range []struct {
		name   string
		values url.Values
	}{{"one_service", largestService}, {"errors", url.Values{"levels": {"error,fatal"}}}, {"substring", url.Values{"contains": {fixtureNeedle}}}} {
		p, err := b.history(ctx, "/logs/rows?"+rangeValues(now.Add(-time.Hour), now.Add(time.Minute), filter.values).Encode())
		check(filter.name, err == nil && p.rows > 0, "%d rows in %.0f ms (%v)", p.rows, ms(p.elapsed), err)
	}
	if target := r.recentJSONEvent(); target >= 0 {
		p, err := b.history(ctx, "/logs/rows?"+rangeValues(now.Add(-2*time.Hour), now.Add(time.Minute), url.Values{"request_id": {requestID(target)}}).Encode())
		check("request_id", err == nil && p.rows == 1, "%d rows for soak-%d (%v)", p.rows, target, err)
	}
	if first.detailURL != "" {
		status, _, elapsed, err := b.do(ctx, http.MethodGet, first.detailURL, nil)
		check("detail", err == nil && status == http.StatusOK, "HTTP %d in %.0f ms", status, ms(elapsed))
	}
	// Three minutes at the base rate stay under the 100,000-row export limit.
	exportWindow := rangeValues(now.Add(-3*time.Minute), now.Add(time.Minute), nil)
	status, bytes, lines, elapsed, err := b.export(ctx, exportWindow)
	check("export", err == nil && status == http.StatusOK && lines > 0, "HTTP %d, %d lines, %d bytes in %.0f ms", status, lines, bytes, ms(elapsed))
	r.soak.Final["critical_queries"] = results
	r.event(cycle, "critical_queries", ok, "%s", strings.Join(results, "; "))
}

// recentJSONEvent returns the sequence number of a JSON event (which
// carries a request ID) in a recently acknowledged batch.
func (r *soakRun) recentJSONEvent() int64 {
	for _, b := range r.in.recentAcked(50, time.Hour) {
		for i := b.From; i < b.To; i++ {
			if r.in.fixture.kind(i) == kindJSON {
				return i
			}
		}
	}
	return -1
}

// restoreDrill restores the final backup into an empty volume, checks it
// offline, starts it, and reads History from it.
func (r *soakRun) restoreDrill(ctx context.Context, cycle int) {
	restoreDir := filepath.Join(r.workdir, "restored")
	backupDir := filepath.Join(r.workdir, "backup")
	for _, dir := range []string{restoreDir, backupDir} {
		_, _ = podman("unshare", "rm", "-rf", dir)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			r.event(cycle, "restore_drill", false, "%v", err)
			return
		}
		if _, err := podman("unshare", "chown", "65532:65532", dir); err != nil {
			r.event(cycle, "restore_drill", false, "%v", err)
			return
		}
	}
	if _, err := podman("unshare", "cp", "-p", filepath.Join(r.dataDir, "soak-final.sqlite"), filepath.Join(backupDir, "soak-final.sqlite")); err != nil {
		r.event(cycle, "restore_drill", false, "copy: %v", err)
		return
	}
	output, err := podmanInput(nil, "run", "--rm", "-v", restoreDir+":/data:Z", "-v", backupDir+":/backup:Z,ro",
		r.image, "restore", "--confirm", "RESTORE", "--replace-unhealthy", "/backup/soak-final.sqlite")
	if err != nil {
		r.event(cycle, "restore_drill", false, "restore: %v", err)
		return
	}
	restored := lastLine(output)
	check, err := podmanInput(nil, "run", "--rm", "-v", restoreDir+":/data:Z", r.image, "database", "check", "--full")
	if err != nil {
		r.event(cycle, "restore_drill", false, "check: %v", err)
		return
	}
	uiPort, ingestPort := r.uiPort+2, r.ingestPort+2
	ui := fmt.Sprintf("http://127.0.0.1:%d", uiPort)
	_, _ = podman("rm", "--force", soakRestoredContainer)
	if _, err := podman("run", "-d", "--name", soakRestoredContainer, "-v", restoreDir+":/data:Z",
		"-p", fmt.Sprintf("127.0.0.1:%d:8080", uiPort), "-p", fmt.Sprintf("127.0.0.1:%d:8081", ingestPort),
		"-e", "SIFTAIL_PUBLIC_URL="+ui, "-e", fmt.Sprintf("SIFTAIL_INGEST_PUBLIC_URL=http://127.0.0.1:%d/api/v1/ingest", ingestPort),
		r.image); err != nil {
		r.event(cycle, "restore_drill", false, "start: %v", err)
		return
	}
	defer func() { _, _ = podman("rm", "--force", soakRestoredContainer) }()
	readyCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	_, err = waitReady(readyCtx, ui)
	cancel()
	if err != nil {
		r.event(cycle, "restore_drill", false, "restored server not ready: %v", err)
		return
	}
	b, err := signIn(ctx, ui)
	if err != nil {
		r.event(cycle, "restore_drill", false, "sign-in on restored server: %v", err)
		return
	}
	now := time.Now()
	p, err := b.history(ctx, "/logs/rows?"+rangeValues(now.Add(-24*time.Hour), now.Add(time.Minute), nil).Encode())
	fields, _ := b.status(ctx)
	r.event(cycle, "restore_drill", err == nil && p.rows == 200,
		"restore into an empty volume: %s; full check: %s; restored server History %d rows, oldest %s, newest %s",
		restored, lastLine(check), p.rows, fields["Oldest event"], fields["Newest event"])
}

func (r *soakRun) cleanupSoak() {
	_, _ = podman("rm", "--force", soakContainer)
	_, _ = podman("rm", "--force", soakRestoredContainer)
	for _, dir := range []string{"data", "verify", "restored", "backup"} {
		_, _ = podman("unshare", "rm", "-rf", filepath.Join(r.workdir, dir))
	}
}
