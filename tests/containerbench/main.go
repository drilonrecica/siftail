// Command containerbench measures a Siftail production container image
// against the PRODUCT.md §15 targets and the 100,000/1,000,000/10,000,000
// event query gate (ARCHITECTURE.md §33). scripts/container-bench.sh runs
// it with the work directory on a real disk; docs/performance/0.5.0-container.md
// records the method and results.
//
// It drives only public interfaces: the image's CLI for setup and backup,
// the ingestion endpoint, and the signed-in browser routes. Nothing it
// writes is committed except the JSON summary.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	containerName = "siftail-container-bench"
	adminUser     = "Admin"
	adminPassword = "siftail-container-bench-password"
)

type phaseResult struct {
	Load    *loadResult   `json:"load,omitempty"`
	Memory  memoryResult  `json:"memory"`
	Disk    diskResult    `json:"disk_after"`
	Readers *readerResult `json:"readers,omitempty"`
	Notes   []string      `json:"notes,omitempty"`
}

type tierResult struct {
	Events       int64         `json:"events"`
	OldestEvent  time.Time     `json:"oldest_event"`
	NewestEvent  time.Time     `json:"newest_event"`
	Disk         diskResult    `json:"disk"`
	BytesPerEvnt float64       `json:"data_dir_bytes_per_event"`
	Queries      []queryResult `json:"queries"`
	Repeat       []queryResult `json:"repeat,omitempty"`
}

type summary struct {
	Started     time.Time               `json:"started"`
	Finished    time.Time               `json:"finished"`
	Environment map[string]string       `json:"environment"`
	Startup     map[string]float64      `json:"startup_seconds"`
	Phases      map[string]*phaseResult `json:"phases"`
	Tiers       []tierResult            `json:"tiers"`
	Backup      map[string]any          `json:"backup,omitempty"`
	Retention   map[string]any          `json:"retention,omitempty"`
	Shutdown    map[string]float64      `json:"shutdown_seconds"`
	Failures    []string                `json:"failures"`
}

func newHTTPClient(timeout time.Duration) *http.Client {
	transport := &http.Transport{
		Proxy:               nil,
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		MaxIdleConns:        256,
		MaxIdleConnsPerHost: 256,
		IdleConnTimeout:     90 * time.Second,
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}

func main() {
	image := flag.String("image", "", "Siftail image to measure")
	workdir := flag.String("workdir", "", "work directory on a real disk")
	out := flag.String("out", "", "JSON summary path")
	quick := flag.Bool("quick", false, "small tiers and short phases to check the harness")
	keep := flag.Bool("keep", false, "keep the container and data directory")
	uiPort := flag.Int("ui-port", 28180, "host UI port")
	ingestPort := flag.Int("ingest-port", 28181, "host ingestion port")
	soak := flag.Bool("soak", false, "run the bounded production soak (docs/performance/0.5.0-soak.md) instead of the benchmark")
	duration := flag.Duration("duration", 3*time.Hour, "soak duration; -quick shortens it to 12 minutes")
	seed := flag.Int64("seed", 55, "soak workload seed")
	holdLock := flag.String("hold-write-lock", "", "internal: hold a SQLite write lock on this database (run by the soak under podman unshare)")
	holdFor := flag.Duration("hold-for", time.Minute, "internal: how long -hold-write-lock holds the lock")
	flag.Parse()
	if *holdLock != "" {
		if err := holdWriteLock(*holdLock, *holdFor); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *image == "" || *workdir == "" || *out == "" {
		flag.Usage()
		os.Exit(2)
	}
	if *soak {
		os.Exit(runSoak(soakOptions{
			image: *image, workdir: *workdir, out: *out, quick: *quick, keep: *keep,
			duration: *duration, seed: *seed, uiPort: *uiPort, ingestPort: *ingestPort,
		}))
	}
	run := &benchRun{
		name:  containerName,
		image: *image, workdir: *workdir, quick: *quick,
		ui:     fmt.Sprintf("http://127.0.0.1:%d", *uiPort),
		ingest: fmt.Sprintf("http://127.0.0.1:%d/api/v1/ingest", *ingestPort),
		uiPort: *uiPort, ingestPort: *ingestPort,
		summary: summary{
			Started: time.Now().UTC(), Environment: map[string]string{},
			Startup: map[string]float64{}, Phases: map[string]*phaseResult{},
			Shutdown: map[string]float64{}, Failures: []string{},
		},
	}
	err := run.execute()
	run.summary.Finished = time.Now().UTC()
	if err != nil {
		run.summary.Failures = append(run.summary.Failures, err.Error())
		log.Printf("FAILED: %v", err)
	}
	encoded, _ := json.MarshalIndent(run.summary, "", "  ")
	if writeErr := os.WriteFile(*out, append(encoded, '\n'), 0o644); writeErr != nil {
		log.Fatal(writeErr)
	}
	if !*keep {
		run.cleanup()
	}
	if err != nil || len(run.summary.Failures) > 0 {
		os.Exit(1)
	}
}

type benchRun struct {
	name                       string // container name
	image, workdir, ui, ingest string
	uiPort, ingestPort         int
	quick                      bool
	dataDir                    string
	pid                        int
	fixture                    fixture
	ingester                   *ingester
	admin                      *browser
	next                       int64 // next fixture index to send
	summary                    summary
}

// plan returns the tier sizes and the events of each rate phase.
func (r *benchRun) plan() (tiers []int64, sustainedSeconds, burstSeconds, saturationSeconds int) {
	if r.quick {
		return []int64{10_000, 50_000, 100_000}, 10, 4, 10
	}
	return []int64{100_000, 1_000_000, 10_000_000}, 300, 60, 60
}

func (r *benchRun) execute() error {
	ctx := context.Background()
	r.recordEnvironment()
	r.dataDir = filepath.Join(r.workdir, "data")
	_, _ = podman("rm", "--force", r.name)
	_, _ = podman("unshare", "rm", "-rf", r.dataDir)
	if err := os.MkdirAll(r.dataDir, 0o750); err != nil {
		return err
	}
	if _, err := podman("unshare", "chown", "65532:65532", r.dataDir); err != nil {
		return err
	}
	if err := r.setup(); err != nil {
		return err
	}
	if err := r.start(ctx, "first_start"); err != nil {
		return err
	}

	// Idle on an empty database: let startup work settle, then sample.
	log.Printf("idle (empty database)")
	time.Sleep(r.seconds(30))
	idle := startSampler(r.pid, time.Second)
	time.Sleep(r.seconds(60))
	r.summary.Phases["idle_empty"] = &phaseResult{Memory: idle.finish(), Disk: diskUsage(r.dataDir)}

	admin, err := newBrowser(r.ui, adminUser, adminPassword)
	if err != nil {
		return err
	}
	r.admin = admin
	// The default 4 GiB size limit is below a 10,000,000-event database;
	// raise it so retention does not delete the fixture while it grows.
	if err := admin.saveRetention(ctx, 14, 64); err != nil {
		return err
	}

	tiers, sustained, burst, saturation := r.plan()
	for index, tier := range tiers {
		if index > 0 {
			// Each tier after the first ends with a sustained phase
			// (1,000 events/s with browser readers) and a burst
			// (5,000 events/s), both counted in the tier.
			rateEvents := int64(sustained*1000 + burst*5000)
			if err := r.fill(ctx, fmt.Sprintf("fill_to_%d", tier-rateEvents), tier-rateEvents); err != nil {
				return err
			}
			if err := r.rate(ctx, fmt.Sprintf("sustained_1000_at_%d", tier), 1000, int64(sustained*1000), true); err != nil {
				return err
			}
			if err := r.rate(ctx, fmt.Sprintf("burst_5000_at_%d", tier), 5000, int64(burst*5000), false); err != nil {
				return err
			}
		} else if err := r.fill(ctx, fmt.Sprintf("fill_to_%d", tier), tier); err != nil {
			return err
		}
		if r.next != tier {
			return fmt.Errorf("tier %d: sent %d events", tier, r.next)
		}
		if err := r.measureTier(ctx, tier, index == len(tiers)-1); err != nil {
			return err
		}
	}

	log.Printf("idle (after load)")
	time.Sleep(r.seconds(30))
	idleLoaded := startSampler(r.pid, time.Second)
	time.Sleep(r.seconds(60))
	r.summary.Phases["idle_after_load"] = &phaseResult{Memory: idleLoaded.finish(), Disk: diskUsage(r.dataDir)}

	if err := r.backup(ctx); err != nil {
		return err
	}
	if err := r.saturate(ctx, saturation); err != nil {
		return err
	}
	if err := r.retention(ctx); err != nil {
		return err
	}
	stopped := time.Now()
	if _, err := podman("stop", "--time", "30", r.name); err != nil {
		return err
	}
	r.summary.Shutdown["final_stop"] = time.Since(stopped).Seconds()
	return nil
}

func (r *benchRun) seconds(n int) time.Duration {
	if r.quick {
		return time.Duration(n) * time.Second / 10
	}
	return time.Duration(n) * time.Second
}

func (r *benchRun) recordEnvironment() {
	env := r.summary.Environment
	command := func(name string, args ...string) string {
		output, err := exec.Command(name, args...).CombinedOutput()
		if err != nil {
			return "unavailable"
		}
		return strings.TrimSpace(string(output))
	}
	env["kernel"] = command("uname", "-sr")
	env["arch"] = runtime.GOARCH
	if cpu, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(cpu), "\n") {
			if strings.HasPrefix(line, "model name") {
				env["cpu"] = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
				break
			}
		}
	}
	env["cpus"] = strconv.Itoa(runtime.NumCPU())
	if memory, err := os.ReadFile("/proc/meminfo"); err == nil {
		env["memory"] = strings.Join(strings.Fields(strings.SplitN(string(memory), "\n", 2)[0])[1:], " ")
	}
	env["podman"] = command("podman", "version", "--format", "{{.Client.Version}}")
	env["image"] = r.image
	env["image_id"] = command("podman", "image", "inspect", r.image, "--format", "{{.Id}}")
	env["image_digest"] = command("podman", "image", "inspect", r.image, "--format", "{{.Digest}}")
	env["image_size_bytes"] = command("podman", "image", "inspect", r.image, "--format", "{{.Size}}")
	env["image_arch"] = command("podman", "image", "inspect", r.image, "--format", "{{.Architecture}}")
	env["image_version"] = command("podman", "image", "inspect", r.image, "--format", `{{index .Labels "org.opencontainers.image.version"}}`)
	env["image_revision"] = command("podman", "image", "inspect", r.image, "--format", `{{index .Labels "org.opencontainers.image.revision"}}`)
	env["workdir_filesystem"] = command("stat", "-f", "-c", "%T", r.workdir)
	env["workdir_device"] = command("df", "--output=source", r.workdir)
	env["container_limits"] = "none (no --memory or --cpus)"
	env["quick"] = strconv.FormatBool(r.quick)
}

func (r *benchRun) runCLI(stdin []byte, args ...string) (string, error) {
	base := []string{"run", "--rm", "-i", "-v", r.dataDir + ":/data:Z", r.image}
	return podmanInput(stdin, append(base, args...)...)
}

func (r *benchRun) setup() error {
	password := []byte(adminPassword + "\n" + adminPassword + "\n")
	if _, err := r.runCLI(password, "admin", "create", "--username", adminUser); err != nil {
		return err
	}
	var tokens []string
	for _, server := range fixtureServers {
		if _, err := r.runCLI(nil, "server", "create", "--name", server.name); err != nil {
			return err
		}
	}
	for index := range fixtureServers {
		output, err := r.runCLI(nil, "token", "create", "--server", strconv.Itoa(index+1), "--name", "bench")
		if err != nil {
			return err
		}
		token := ""
		for _, line := range strings.Split(output, "\n") {
			if value, ok := strings.CutPrefix(line, "token (shown once): "); ok {
				token = strings.TrimSpace(value)
			}
		}
		if token == "" {
			return errors.New("token create printed no token")
		}
		tokens = append(tokens, token)
	}
	r.fixture = newFixture(time.Now())
	r.ingester = &ingester{url: r.ingest, tokens: tokens, client: newHTTPClient(2 * time.Minute), fixture: r.fixture}
	return nil
}

func (r *benchRun) start(ctx context.Context, label string) error {
	_, _ = podman("rm", "--force", r.name)
	started := time.Now()
	if _, err := podman("run", "-d", "--name", r.name,
		"-v", r.dataDir+":/data:Z",
		"-p", fmt.Sprintf("127.0.0.1:%d:8080", r.uiPort),
		"-p", fmt.Sprintf("127.0.0.1:%d:8081", r.ingestPort),
		"-e", "SIFTAIL_PUBLIC_URL="+r.ui,
		"-e", "SIFTAIL_INGEST_PUBLIC_URL="+r.ingest,
		r.image); err != nil {
		return err
	}
	return r.awaitStart(ctx, label, started)
}

func (r *benchRun) awaitStart(ctx context.Context, label string, started time.Time) error {
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if _, err := waitReady(waitCtx, r.ui); err != nil {
		logs, _ := podman("logs", r.name)
		return fmt.Errorf("container not ready: %w\n%s", err, logs)
	}
	r.summary.Startup[label] = time.Since(started).Seconds()
	pid, err := podman("inspect", "--format", "{{.State.Pid}}", r.name)
	if err != nil {
		return err
	}
	r.pid, err = strconv.Atoi(strings.TrimSpace(pid))
	return err
}

func (r *benchRun) fill(ctx context.Context, name string, to int64) error {
	log.Printf("%s: events %d..%d", name, r.next, to)
	return r.phase(ctx, name, loadSpec{name: name, from: r.next, to: to, batch: 100, workers: 8, retry: true}, false)
}

func (r *benchRun) rate(ctx context.Context, name string, rate float64, events int64, readers bool) error {
	log.Printf("%s: %d events at %.0f/s", name, events, rate)
	return r.phase(ctx, name, loadSpec{name: name, from: r.next, to: r.next + events, rate: rate, batch: 100, workers: 32, retry: true}, readers)
}

func (r *benchRun) phase(ctx context.Context, name string, spec loadSpec, readers bool) error {
	memory := startSampler(r.pid, time.Second)
	var readerDone func() *readerResult
	if readers {
		var err error
		readerDone, err = r.startReaders(ctx)
		if err != nil {
			return err
		}
	}
	result := r.ingester.run(ctx, spec)
	phase := &phaseResult{Load: &result}
	if readerDone != nil {
		phase.Readers = readerDone()
	}
	phase.Memory = memory.finish()
	phase.Disk = diskUsage(r.dataDir)
	r.summary.Phases[name] = phase
	log.Print(result.String())
	log.Printf("  rss max %.1f MiB mean %.1f MiB; cpu %.2f cores", phase.Memory.RSSMaxMiB, phase.Memory.RSSMeanMiB, phase.Memory.CPUUtilization)
	if result.Events != spec.to-spec.from {
		return fmt.Errorf("%s: %d of %d events acknowledged", name, result.Events, spec.to-spec.from)
	}
	r.next = spec.to
	return nil
}

type readerResult struct {
	Sessions       int         `json:"browser_sessions"`
	HistoryReads   percentiles `json:"history_read_ms"`
	HistoryErrors  int         `json:"history_errors"`
	LiveStreams    int         `json:"live_streams"`
	LiveFrames     []int64     `json:"live_frames"`
	LiveErrors     []string    `json:"live_errors,omitempty"`
	ExportDuringMS float64     `json:"export_during_load_ms,omitempty"`
}

// startReaders signs in four browser sessions; each reads a History page
// every second and holds one Live stream, the "several active browser
// clients" of PRODUCT.md §15.1.
func (r *benchRun) startReaders(ctx context.Context) (func() *readerResult, error) {
	const sessions = 4
	readerCtx, cancel := context.WithCancel(ctx)
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		latencies []time.Duration
		result    = &readerResult{Sessions: sessions, LiveStreams: sessions, LiveFrames: make([]int64, sessions)}
	)
	for index := range sessions {
		b, err := newBrowser(r.ui, adminUser, adminPassword)
		if err != nil {
			cancel()
			return nil, err
		}
		wg.Add(2)
		go func() {
			defer wg.Done()
			frames, err := b.live(readerCtx)
			mu.Lock()
			result.LiveFrames[index] = frames
			if err != nil {
				result.LiveErrors = append(result.LiveErrors, err.Error())
			}
			mu.Unlock()
		}()
		go func() {
			defer wg.Done()
			scopes := []url.Values{nil, largestService, {"levels": {"error,fatal"}}, partialApp}
			for turn := 0; readerCtx.Err() == nil; turn++ {
				// The newest hour up to now: the events this phase is
				// writing.
				now := time.UnixMicro(r.fixture.timestampUS(r.next)).Add(time.Hour)
				values := rangeValues(now.Add(-2*time.Hour), now, scopes[(index+turn)%len(scopes)])
				p, err := b.history(readerCtx, "/logs/rows?"+values.Encode())
				mu.Lock()
				if err != nil {
					if readerCtx.Err() == nil {
						result.HistoryErrors++
					}
				} else {
					latencies = append(latencies, p.elapsed)
				}
				mu.Unlock()
				select {
				case <-readerCtx.Done():
				case <-time.After(time.Second):
				}
			}
		}()
	}
	return func() *readerResult {
		cancel()
		wg.Wait()
		result.HistoryReads = summarize(latencies)
		return result
	}, nil
}

func (r *benchRun) measureTier(ctx context.Context, events int64, last bool) error {
	log.Printf("queries at %d events", events)
	// Let the WAL checkpoint and caches settle as an operator's first query
	// after a quiet moment would see them.
	time.Sleep(r.seconds(20))
	t := r.fixture.tier(events)
	repetitions := 7
	queries, err := runQueries(ctx, r.admin, r.fixture, t, repetitions)
	if err != nil {
		return err
	}
	result := tierResult{
		Events: events, OldestEvent: t.oldest, NewestEvent: t.newest,
		Disk: diskUsage(r.dataDir), Queries: queries,
	}
	if last {
		// A second pass at the top tier measures variance.
		if result.Repeat, err = runQueries(ctx, r.admin, r.fixture, t, repetitions); err != nil {
			return err
		}
	}
	result.BytesPerEvnt = float64(result.Disk.DataDirBytes) / float64(events)
	for _, query := range append(append([]queryResult{}, queries...), result.Repeat...) {
		log.Printf("  %-32s first %8.1f ms  warm median %8.1f ms  [%s]", query.Name, query.FirstMS, query.Median, query.Check)
		if strings.HasPrefix(query.Check, "FAILED") || strings.HasPrefix(query.Check, "unexpected") {
			r.summary.Failures = append(r.summary.Failures, fmt.Sprintf("tier %d %s: %s", events, query.Name, query.Check))
		}
	}
	r.summary.Tiers = append(r.summary.Tiers, result)
	return nil
}

// backup takes a full online backup while the sustained load continues,
// then verifies it.
func (r *benchRun) backup(ctx context.Context) error {
	log.Printf("backup during sustained ingestion")
	loadCtx, cancel := context.WithCancel(ctx)
	memory := startSampler(r.pid, time.Second)
	var load loadResult
	var wg sync.WaitGroup
	wg.Add(1)
	from := r.next
	go func() {
		defer wg.Done()
		// Open-ended: the context ends the phase once the backup is done.
		load = r.ingester.run(loadCtx, loadSpec{name: "sustained_1000_during_backup", from: from, to: from + 100_000_000, rate: 1000, batch: 100, workers: 32, retry: true})
	}()
	time.Sleep(r.seconds(10))
	started := time.Now()
	output, err := podman("exec", r.name, "/siftail", "backup", "--output", "/data/bench-backup.sqlite")
	backupSeconds := time.Since(started).Seconds()
	time.Sleep(r.seconds(10))
	cancel()
	wg.Wait()
	r.next = from + load.Events + 1_000 // skip indices a cancelled batch may have half-sent
	phase := &phaseResult{Load: &load, Memory: memory.finish(), Disk: diskUsage(r.dataDir)}
	r.summary.Phases["backup_during_ingest"] = phase
	log.Print(load.String())
	if err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	size, _ := podman("unshare", "stat", "-c", "%s", filepath.Join(r.dataDir, "bench-backup.sqlite"))
	started = time.Now()
	verify, verifyErr := podman("exec", r.name, "/siftail", "backup", "verify", "/data/bench-backup.sqlite")
	verifySeconds := time.Since(started).Seconds()
	r.summary.Backup = map[string]any{
		"backup_seconds": backupSeconds, "verify_seconds": verifySeconds,
		"backup_bytes": strings.TrimSpace(size), "backup_output": strings.TrimSpace(output),
		"verify_output": strings.TrimSpace(verify),
	}
	_, _ = podman("unshare", "rm", "-f", filepath.Join(r.dataDir, "bench-backup.sqlite"))
	if verifyErr != nil {
		return fmt.Errorf("backup verify: %w", verifyErr)
	}
	log.Printf("  backup %.1fs (%s bytes), verify %.1fs", backupSeconds, strings.TrimSpace(size), verifySeconds)
	return nil
}

// saturate offers far more than the writer can commit, without retries,
// to observe bounded rejection and memory at the queue limit.
func (r *benchRun) saturate(ctx context.Context, seconds int) error {
	log.Printf("queue saturation for %ds", seconds)
	loadCtx, cancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
	defer cancel()
	memory := startSampler(r.pid, 250*time.Millisecond)
	from := r.next
	load := r.ingester.run(loadCtx, loadSpec{name: "queue_saturation", from: from, to: from + 100_000_000, batch: 1000, workers: 64, sameServer: true})
	phase := &phaseResult{Load: &load, Memory: memory.finish()}
	r.next = from + int64(load.Requests+load.DroppedBatches+64)*1000
	readyCtx, readyCancel := context.WithTimeout(ctx, 2*time.Minute)
	defer readyCancel()
	recovery, err := waitReady(readyCtx, r.ui)
	if err != nil {
		return fmt.Errorf("not ready after saturation: %w", err)
	}
	phase.Notes = append(phase.Notes, fmt.Sprintf("ready %.2fs after the load stopped", recovery.Seconds()))
	if fields, err := r.admin.status(ctx); err == nil {
		phase.Notes = append(phase.Notes, "status: rejected batches "+fields["Rejected batches"]+", queued events "+fields["Queued events"])
	}
	phase.Disk = diskUsage(r.dataDir)
	r.summary.Phases["queue_saturation"] = phase
	log.Print(load.String())
	log.Printf("  rss max %.1f MiB", phase.Memory.RSSMaxMiB)
	return nil
}

// retention lowers the size limit below the database footprint, restarts
// (cleanup runs at startup, then hourly), and times the size-triggered
// cleanup while ingestion continues at 1,000 events/s.
func (r *benchRun) retention(ctx context.Context) error {
	before := diskUsage(r.dataDir)
	limitGiB := int(math.Max(1, math.Floor(float64(before.DatabaseBytes)/(1<<30)/2)))
	log.Printf("retention: size limit %d GiB against a %.2f GiB database", limitGiB, float64(before.DatabaseBytes)/(1<<30))
	if err := r.admin.saveRetention(ctx, 14, limitGiB); err != nil {
		return err
	}
	stopped := time.Now()
	if _, err := podman("stop", "--time", "30", r.name); err != nil {
		return err
	}
	r.summary.Shutdown["restart_for_retention"] = time.Since(stopped).Seconds()
	started := time.Now()
	if _, err := podman("start", r.name); err != nil {
		return err
	}
	if err := r.awaitStart(ctx, "restart_before_retention", started); err != nil {
		return err
	}
	admin, err := newBrowser(r.ui, adminUser, adminPassword)
	if err != nil {
		return err
	}
	r.admin = admin
	loadCtx, cancel := context.WithCancel(ctx)
	memory := startSampler(r.pid, time.Second)
	var load loadResult
	var wg sync.WaitGroup
	wg.Add(1)
	from := r.next
	go func() {
		defer wg.Done()
		load = r.ingester.run(loadCtx, loadSpec{name: "sustained_1000_during_retention", from: from, to: from + 100_000_000, rate: 1000, batch: 100, workers: 32, retry: true})
	}()
	var fields map[string]string
	deadline := time.Now().Add(60 * time.Minute)
	for time.Now().Before(deadline) {
		fields, err = admin.status(ctx)
		if err == nil && fields["Last cleanup"] != "" && fields["Last cleanup"] != "Not yet run" {
			break
		}
		time.Sleep(2 * time.Second)
	}
	cleanupSeconds := time.Since(started).Seconds()
	time.Sleep(r.seconds(10))
	cancel()
	wg.Wait()
	r.next = from + load.Events + 1_000
	after := diskUsage(r.dataDir)
	r.summary.Phases["retention_during_ingest"] = &phaseResult{Load: &load, Memory: memory.finish(), Disk: after}
	r.summary.Retention = map[string]any{
		"limit_gib": limitGiB, "disk_before": before, "disk_after": after,
		"seconds_from_start_to_cleanup_recorded": cleanupSeconds,
		"last_cleanup":                           fields["Last cleanup"], "cleanup_result": fields["Cleanup result"],
		"oldest_event_after": fields["Oldest event"], "database_after": fields["Database"],
	}
	log.Print(load.String())
	log.Printf("  cleanup after %.0fs: %s", cleanupSeconds, fields["Cleanup result"])
	return nil
}

func (r *benchRun) cleanup() {
	_, _ = podman("rm", "--force", r.name)
	_, _ = podman("unshare", "rm", "-rf", r.dataDir)
}
