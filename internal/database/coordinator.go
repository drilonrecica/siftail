package database

import (
	"context"
	"database/sql"
	"errors"
	"sync"
)

const mutationQueueCapacity = 64

var ErrCoordinatorClosed = errors.New("database mutation coordinator is closed")

// MutationCoordinator is the application mutation boundary used by feature
// stores. Implementations execute one short transaction at a time.
type MutationCoordinator interface {
	Do(context.Context, func(*sql.Tx) error) error
}

type mutationOperation struct {
	run    func(*sql.Tx) error
	result chan error
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
			operation.result <- c.execute(context.Background(), operation.run)
		case <-ctx.Done():
			c.Close()
			c.drain(context.Background())
			return nil
		case <-c.closing:
			c.drain(context.Background())
			return nil
		}
	}
}

func (c *Coordinator) drain(ctx context.Context) {
	for {
		select {
		case operation := <-c.operations:
			operation.result <- c.execute(ctx, operation.run)
		default:
			return
		}
	}
}

func (c *Coordinator) Ready() <-chan struct{} {
	return c.ready
}

func (c *Coordinator) execute(ctx context.Context, run func(*sql.Tx) error) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return classify("begin coordinated mutation", err)
	}
	if err := run(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return classify("commit coordinated mutation", err)
	}
	return nil
}

// Do admits one operation and waits for its committed result. Once admitted,
// caller cancellation does not remove the operation from the serialized path.
func (c *Coordinator) Do(ctx context.Context, run func(*sql.Tx) error) error {
	if run == nil {
		return errors.New("database mutation operation is nil")
	}
	operation := mutationOperation{run: run, result: make(chan error, 1)}

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

	select {
	case err := <-operation.result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
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
