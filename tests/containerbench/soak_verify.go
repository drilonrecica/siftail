package main

import (
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// soakVerification compares a copy of the final backup with the harness's
// record of every batch.
type soakVerification struct {
	Integrity        string `json:"integrity_check"`
	ForeignKeyErrors int    `json:"foreign_key_errors"`
	StoredEvents     int64  `json:"stored_events"`
	SoakEvents       int64  `json:"stored_soak_events"`
	OtherEvents      int64  `json:"stored_other_events"`
	Sources          int64  `json:"sources"`
	Containers       int64  `json:"container_instances"`
	// RetentionCutoffUS is the oldest received_at_us still stored: size
	// retention deleted everything received before it.
	RetentionCutoffUS int64 `json:"retention_cutoff_received_at_us"`

	BatchesAcknowledged     int64 `json:"batches_acknowledged"`
	BatchesComplete         int64 `json:"acknowledged_batches_complete"`
	RetentionDeletedBatches int64 `json:"acknowledged_batches_deleted_by_retention"`
	RetentionPartialBatches int64 `json:"acknowledged_batches_cut_by_retention"`
	LostBatches             int64 `json:"acknowledged_batches_lost"`
	LostEvents              int64 `json:"acknowledged_events_lost"`
	DuplicateEvents         int64 `json:"duplicate_events"`
	PhantomEvents           int64 `json:"phantom_events"`
	RejectedButStored       int64 `json:"rejected_batches_stored"`
	PartialBatches          int64 `json:"non_atomic_batches"`
	OrderViolations         int64 `json:"intra_batch_order_violations"`
	ConflictingContent      int64 `json:"conflicting_replays_stored"`
	AmbiguousStored         int64 `json:"ambiguous_batches_stored"`
	AmbiguousAbsent         int64 `json:"ambiguous_batches_absent"`
	ReleaseSources          int64 `json:"release_sources"`

	Problems []string `json:"problems"`
}

func (v *soakVerification) String() string {
	return fmt.Sprintf("integrity %s; %d events stored (%d soak); %d acknowledged batches: %d complete, %d deleted and %d cut by size retention, %d lost; %d duplicates, %d phantoms, %d order violations, %d non-atomic, %d rejected-but-stored, %d conflicting replays stored",
		v.Integrity, v.StoredEvents, v.SoakEvents, v.BatchesAcknowledged, v.BatchesComplete,
		v.RetentionDeletedBatches, v.RetentionPartialBatches, v.LostBatches, v.DuplicateEvents,
		v.PhantomEvents, v.OrderViolations, v.PartialBatches, v.RejectedButStored, v.ConflictingContent)
}

func verifySoak(path string, in *soakIngester) (*soakVerification, error) {
	db, err := sql.Open("sqlite3", "file:"+path+"?mode=ro&immutable=1")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	v := &soakVerification{}
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&v.Integrity); err != nil {
		return nil, err
	}
	if v.Integrity != "ok" {
		v.Problems = append(v.Problems, "integrity_check: "+v.Integrity)
	}
	rows, err := db.Query("PRAGMA foreign_key_check")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		v.ForeignKeyErrors++
	}
	rows.Close()
	if v.ForeignKeyErrors > 0 {
		v.Problems = append(v.Problems, fmt.Sprintf("%d foreign key errors", v.ForeignKeyErrors))
	}
	for query, target := range map[string]*int64{
		"SELECT count(*) FROM log_events":                                                                    &v.StoredEvents,
		"SELECT count(*) FROM sources":                                                                       &v.Sources,
		"SELECT count(*) FROM container_instances":                                                           &v.Containers,
		"SELECT count(*) FROM sources WHERE service_key LIKE 'release-%'":                                    &v.ReleaseSources,
		"SELECT count(*) FROM log_events WHERE message_text LIKE '%(conflicting replay)%'":                   &v.ConflictingContent,
		"SELECT count(*) FROM log_events WHERE source_event_id IS NULL OR source_event_id NOT LIKE 'soak-%'": &v.OtherEvents,
	} {
		if err := db.QueryRow(query).Scan(target); err != nil {
			return nil, fmt.Errorf("%s: %w", query, err)
		}
	}
	if v.ConflictingContent > 0 {
		v.Problems = append(v.Problems, fmt.Sprintf("%d events stored with conflicting replay content", v.ConflictingContent))
	}

	in.mu.Lock()
	defer in.mu.Unlock()
	limit := in.next.Load()
	ids := make([]int64, limit)
	received := make([]int64, limit)
	rows, err = db.Query("SELECT id, received_at_us, source_event_id FROM log_events WHERE source_event_id LIKE 'soak-%'")
	if err != nil {
		return nil, err
	}
	v.RetentionCutoffUS = 1<<63 - 1
	for rows.Next() {
		var id, at int64
		var eventID string
		if err := rows.Scan(&id, &at, &eventID); err != nil {
			rows.Close()
			return nil, err
		}
		v.SoakEvents++
		v.RetentionCutoffUS = min(v.RetentionCutoffUS, at)
		seq, err := strconv.ParseInt(strings.TrimPrefix(eventID, "soak-"), 10, 64)
		if err != nil || seq < 0 || seq >= limit {
			v.PhantomEvents++
			continue
		}
		if ids[seq] != 0 {
			v.DuplicateEvents++
			continue
		}
		ids[seq], received[seq] = id, at
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	covered := make([]bool, limit)
	for _, b := range in.batches {
		present := int64(0)
		ordered := true
		previous := int64(0)
		for i := b.From; i < b.To; i++ {
			covered[i] = true
			if ids[i] == 0 {
				continue
			}
			present++
			if ids[i] <= previous {
				ordered = false
			}
			previous = ids[i]
		}
		n := b.events()
		// Siftail stamps received_at_us after the attempt began, so a
		// batch whose acknowledged attempt began after the cutoff was
		// received after it and must still be stored.
		retentionCanExplain := b.LastSend.UnixMicro() <= v.RetentionCutoffUS
		switch {
		case !b.Acked.IsZero():
			v.BatchesAcknowledged++
			switch {
			case present == n:
				v.BatchesComplete++
			case retentionCanExplain && present == 0:
				v.RetentionDeletedBatches++
			case retentionCanExplain:
				v.RetentionPartialBatches++
			default:
				v.LostBatches++
				v.LostEvents += n - present
				if v.LostBatches <= 10 {
					v.Problems = append(v.Problems, fmt.Sprintf("acknowledged batch soak-%d..%d (sent %s, acknowledged %s) has %d of %d events",
						b.From, b.To-1, b.LastSend.UTC().Format(time.RFC3339Nano), b.Acked.UTC().Format(time.RFC3339Nano), present, n))
				}
			}
		case b.Rejected:
			if present > 0 {
				v.RejectedButStored++
			}
		default:
			if present > 0 {
				v.AmbiguousStored++
			} else {
				v.AmbiguousAbsent++
			}
		}
		if present > 0 && present < n && !retentionCanExplain {
			v.PartialBatches++
		}
		if present == n && !ordered {
			v.OrderViolations++
		}
	}
	for seq, id := range ids {
		if id != 0 && !covered[seq] {
			v.PhantomEvents++
		}
	}
	for _, check := range []struct {
		count int64
		what  string
	}{
		{v.LostBatches, "acknowledged batches lost"},
		{v.DuplicateEvents, "duplicate events"},
		{v.PhantomEvents, "events stored that the harness never sent"},
		{v.RejectedButStored, "rejected batches stored"},
		{v.PartialBatches, "batches stored partially"},
		{v.OrderViolations, "batches stored out of order"},
	} {
		if check.count > 0 {
			v.Problems = append(v.Problems, fmt.Sprintf("%d %s", check.count, check.what))
		}
	}
	return v, nil
}

// holdWriteLock takes SQLite's write lock on a live database (BEGIN
// IMMEDIATE), prints "locked", holds it, and rolls back. The soak runs it
// inside the container's user namespace to create a controlled, temporary
// database-unavailable condition without touching the data.
func holdWriteLock(path string, hold time.Duration) error {
	db, err := sql.Open("sqlite3", "file:"+path+"?_busy_timeout=30000&_txlock=immediate")
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("take the write lock: %w", err)
	}
	fmt.Fprintln(os.Stdout, "locked")
	time.Sleep(hold)
	return tx.Rollback()
}
