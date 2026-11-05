package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"

	"github.com/mattn/go-sqlite3"
)

const mutationQueueCapacity = 64

var ErrCoordinatorClosed = errors.New("database mutation coordinator is closed")

// MutationCoordinator is the application mutation boundary used by feature
// stores. Implementations execute one short transaction at a time.
type MutationCoordinator interface {
	Do(context.Context, func(*sql.Tx) error) error
}

// MaintenanceCoordinator additionally serializes bounded writer-connection
// maintenance that SQLite does not permit inside a transaction.
type MaintenanceCoordinator interface {
	MutationCoordinator
	DoMaintenance(context.Context, func(*sql.DB) error) error
}

// Statements is the statement surface shared by *sql.Tx and the pinned
// connection of an immediate transaction. A statement prepared on it belongs
// to that connection and must be closed before the transaction ends.
type Statements interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
	PrepareContext(context.Context, string) (*sql.Stmt, error)
}

type mutationOperation struct {
	run         func(*sql.Tx) error
	immediate   func(Statements) error
	maintenance func(*sql.DB) error
	result      chan error
}

// Coordinator serializes every mutation performed by an active server. Its
// fixed-size operation channel provides bounded administrative backpressure.
type Coordinator struct {
	db         *sql.DB
	operations chan mutationOperation
	ready      chan struct{}
	closing    chan struct{}
	done       chan struct{}

	mu         sync.RWMutex
	submitters sync.WaitGroup
	started    bool
	closed     bool
	once       sync.Once
}

func NewCoordinator(db *sql.DB) *Coordinator {
	return &Coordinator{
		db:         db,
		operations: make(chan mutationOperation, mutationQueueCapacity),
		ready:      make(chan struct{}),
		closing:    make(chan struct{}),
		done:       make(chan struct{}),
	}
}

// Run owns the coordinator worker. Closing admission drains every operation
// accepted before Close and then returns.
//
// Both shutdown paths call Close before draining. Close returns only after
// every submitter that passed admission has either enqueued its operation or
// given up, and sync.Once makes a concurrent caller wait for that too, so the
// drain sees every admitted operation and none is left without a result.
func (c *Coordinator) Run(ctx context.Context) error {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return errors.New("database mutation coordinator already started")
	}
	c.started = true
	close(c.ready)
	c.mu.Unlock()
	defer close(c.done)

	for {
		select {
		case operation := <-c.operations:
			operation.result <- c.executeOperation(context.Background(), operation)
		case <-ctx.Done():
			c.Close()
			c.drain(context.Background())
			return nil
		case <-c.closing:
			c.Close()
			c.drain(context.Background())
			return nil
		}
	}
}

func (c *Coordinator) drain(ctx context.Context) {
	for {
		select {
		case operation := <-c.operations:
			operation.result <- c.executeOperation(ctx, operation)
		default:
			return
		}
	}
}

func (c *Coordinator) Ready() <-chan struct{} {
	return c.ready
}

func (c *Coordinator) executeOperation(ctx context.Context, operation mutationOperation) error {
	if operation.maintenance != nil {
		return c.executeMaintenance(operation.maintenance)
	}
	if operation.immediate != nil {
		return c.executeImmediate(ctx, operation.immediate)
	}
	return c.execute(ctx, operation.run)
}

func (c *Coordinator) execute(ctx context.Context, run func(*sql.Tx) error) error {
	if run == nil {
		return errors.New("database mutation operation is nil")
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return classify("begin coordinated mutation", err)
	}
	if err := run(tx); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return errors.Join(err, classify("roll back coordinated mutation", rollbackErr))
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return classify("commit coordinated mutation", err)
	}
	return nil
}

// executeImmediate runs one BEGIN IMMEDIATE transaction on a pinned writer
// connection. database/sql always begins with the driver's deferred BEGIN, so
// the transaction statements are issued explicitly; the caller may then use
// SAVEPOINTs freely inside it (ADR 0009).
func (c *Coordinator) executeImmediate(ctx context.Context, run func(Statements) error) error {
	conn, err := c.db.Conn(ctx)
	if err != nil {
		return classify("acquire writer connection", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return classify("begin immediate mutation", err)
	}
	if err := run(conn); err != nil {
		abandonTransaction(ctx, conn)
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		// A failed COMMIT may leave the transaction open (SQLITE_BUSY);
		// roll it back so the connection returns to autocommit.
		abandonTransaction(ctx, conn)
		return classify("commit immediate mutation", err)
	}
	return nil
}

// abandonTransaction rolls back whatever is still open. SQLite may already have
// rolled the transaction back itself (I/O, full, or busy errors), in which case
// ROLLBACK reports that no transaction is active; the outcome that matters is
// the connection's state afterwards. A connection that is somehow still inside
// a transaction is discarded rather than returned to the pool, so the next
// mutation never inherits it.
func abandonTransaction(ctx context.Context, conn *sql.Conn) {
	_, rollbackErr := conn.ExecContext(ctx, "ROLLBACK")
	if rollbackErr == nil {
		return
	}
	// Raw's only error is the driver.ErrBadConn returned below, which is the
	// instruction to database/sql to discard the connection, not a failure.
	_ = conn.Raw(func(driverConn any) error {
		if sqliteConn, ok := driverConn.(*sqlite3.SQLiteConn); ok && sqliteConn.AutoCommit() {
			return nil
		}
		return driver.ErrBadConn
	})
}

func (c *Coordinator) executeMaintenance(run func(*sql.DB) error) error {
	if run == nil {
		return errors.New("database maintenance operation is nil")
	}
	return run(c.db)
}

// Do admits one operation and waits for its committed result. Caller
// cancellation only abandons admission: once admitted, the operation runs and
// Do reports its real outcome, so a committed mutation is never reported as
// canceled.
func (c *Coordinator) Do(ctx context.Context, run func(*sql.Tx) error) error {
	if run == nil {
		return errors.New("database mutation operation is nil")
	}
	return c.submit(ctx, mutationOperation{run: run, result: make(chan error, 1)})
}

// DoImmediate admits one BEGIN IMMEDIATE transaction and waits for its
// committed result. run receives the statements of that transaction and may
// open SAVEPOINTs inside it, but must not COMMIT, ROLLBACK, or BEGIN; returning
// an error rolls the whole transaction back. Once admitted it follows the same
// completion semantics as Do.
func (c *Coordinator) DoImmediate(ctx context.Context, run func(Statements) error) error {
	if run == nil {
		return errors.New("database mutation operation is nil")
	}
	return c.submit(ctx, mutationOperation{immediate: run, result: make(chan error, 1)})
}

// DoMaintenance orders one bounded non-transactional writer operation with
// mutations. Once admitted it follows the same completion semantics as Do.
func (c *Coordinator) DoMaintenance(ctx context.Context, run func(*sql.DB) error) error {
	if run == nil {
		return errors.New("database maintenance operation is nil")
	}
	return c.submit(ctx, mutationOperation{
		maintenance: run, result: make(chan error, 1),
	})
}

func (c *Coordinator) submit(ctx context.Context, operation mutationOperation) error {
	c.mu.RLock()
	if !c.started || c.closed {
		c.mu.RUnlock()
		return ErrCoordinatorClosed
	}
	c.submitters.Add(1)
	c.mu.RUnlock()
	select {
	case c.operations <- operation:
		c.submitters.Done()
	case <-ctx.Done():
		c.submitters.Done()
		return ctx.Err()
	case <-c.closing:
		c.submitters.Done()
		return ErrCoordinatorClosed
	}

	// Run drains every admitted operation, even during shutdown, so the
	// result always arrives. Returning ctx.Err() here instead would report a
	// mutation that still commits as canceled.
	return <-operation.result
}

// Close rejects new operations and lets Run drain accepted work.
func (c *Coordinator) Close() {
	c.once.Do(func() {
		c.mu.Lock()
		c.closed = true
		close(c.closing)
		c.mu.Unlock()
		c.submitters.Wait()
	})
}

func (c *Coordinator) Wait() {
	<-c.done
}

// Classify converts a driver error into Siftail's safe database categories.
func Classify(operation string, err error) error {
	return classify(operation, err)
}
