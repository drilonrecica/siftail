package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

func podman(args ...string) (string, error) {
	return podmanInput(nil, args...)
}

func podmanInput(stdin []byte, args ...string) (string, error) {
	command := exec.Command("podman", args...)
	if stdin != nil {
		command.Stdin = bytes.NewReader(stdin)
	}
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()
	if err != nil {
		return output.String(), fmt.Errorf("podman %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(output.String()))
	}
	return output.String(), nil
}

// memorySample is read from the host's /proc for the container's main
// process: the Siftail binary is the entrypoint, with no shell or init.
type memorySample struct {
	rssKiB int64
	hwmKiB int64
	cpu    float64 // user+system seconds
}

func readProcess(pid int) (memorySample, error) {
	var sample memorySample
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return sample, err
	}
	for _, line := range strings.Split(string(status), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, _ := strconv.ParseInt(fields[1], 10, 64)
		switch fields[0] {
		case "VmRSS:":
			sample.rssKiB = value
		case "VmHWM:":
			sample.hwmKiB = value
		}
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return sample, err
	}
	// Fields after the parenthesised command name; utime and stime are
	// fields 14 and 15 of the whole line, in clock ticks (USER_HZ = 100 on
	// Linux).
	text := string(stat)
	rest := strings.Fields(text[strings.LastIndexByte(text, ')')+2:])
	if len(rest) > 12 {
		user, _ := strconv.ParseFloat(rest[11], 64)
		system, _ := strconv.ParseFloat(rest[12], 64)
		sample.cpu = (user + system) / 100
	}
	return sample, nil
}

type memoryResult struct {
	Samples        int     `json:"samples"`
	RSSMaxMiB      float64 `json:"rss_max_mib"`
	RSSMeanMiB     float64 `json:"rss_mean_mib"`
	RSSLastMiB     float64 `json:"rss_last_mib"`
	HWMMiB         float64 `json:"high_water_mib"`
	CPUSeconds     float64 `json:"cpu_seconds"`
	WallSeconds    float64 `json:"wall_seconds"`
	CPUUtilization float64 `json:"cpu_cores_mean"`
}

// sampler records RSS once per interval until stopped.
type sampler struct {
	pid    int
	stop   chan struct{}
	done   chan memoryResult
	start  time.Time
	first  memorySample
	once   sync.Once
	result memoryResult
}

func startSampler(pid int, interval time.Duration) *sampler {
	s := &sampler{pid: pid, stop: make(chan struct{}), done: make(chan memoryResult, 1), start: time.Now()}
	s.first, _ = readProcess(pid)
	go func() {
		var total, maximum, last int64
		var count int
		var latest memorySample
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if sample, err := readProcess(pid); err == nil {
				latest = sample
				total += sample.rssKiB
				count++
				last = sample.rssKiB
				if sample.rssKiB > maximum {
					maximum = sample.rssKiB
				}
			}
			select {
			case <-s.stop:
				wall := time.Since(s.start).Seconds()
				result := memoryResult{
					Samples: count, RSSMaxMiB: mib(maximum), RSSLastMiB: mib(last),
					HWMMiB: mib(latest.hwmKiB), CPUSeconds: latest.cpu - s.first.cpu,
					WallSeconds: wall,
				}
				if count > 0 {
					result.RSSMeanMiB = mib(total / int64(count))
				}
				if wall > 0 {
					result.CPUUtilization = result.CPUSeconds / wall
				}
				s.done <- result
				return
			case <-ticker.C:
			}
		}
	}()
	return s
}

func (s *sampler) finish() memoryResult {
	s.once.Do(func() {
		close(s.stop)
		s.result = <-s.done
	})
	return s.result
}

func mib(kib int64) float64 {
	return float64(kib) / 1024
}

type diskResult struct {
	DatabaseBytes int64 `json:"database_bytes"`
	WALBytes      int64 `json:"wal_bytes"`
	SHMBytes      int64 `json:"shm_bytes"`
	DataDirBytes  int64 `json:"data_dir_bytes"`
}

// diskUsage reads the bind-mounted data directory. It belongs to the
// container's 65532 user, so the read runs inside podman's user namespace.
func diskUsage(dataDir string) diskResult {
	var result diskResult
	output, _ := podman("unshare", "sh", "-c",
		`for f in "$1"/siftail.db "$1"/siftail.db-wal "$1"/siftail.db-shm; do `+
			`if [ -e "$f" ]; then stat -c '%n %s' "$f"; else echo "$f 0"; fi; done; du -sb "$1"`,
		"sh", dataDir)
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		name, value := fields[0], fields[1]
		if strings.HasPrefix(name, "/") && strings.HasSuffix(name, "siftail.db") {
			result.DatabaseBytes, _ = strconv.ParseInt(value, 10, 64)
		} else if strings.HasSuffix(name, "-wal") {
			result.WALBytes, _ = strconv.ParseInt(value, 10, 64)
		} else if strings.HasSuffix(name, "-shm") {
			result.SHMBytes, _ = strconv.ParseInt(value, 10, 64)
		} else {
			// du prints size first.
			result.DataDirBytes, _ = strconv.ParseInt(name, 10, 64)
		}
	}
	return result
}

func waitReady(ctx context.Context, base string) (time.Duration, error) {
	started := time.Now()
	client := newHTTPClient(2 * time.Second)
	for ctx.Err() == nil {
		response, err := client.Get(base + "/health/ready")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == 200 {
				return time.Since(started), nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return time.Since(started), ctx.Err()
}
