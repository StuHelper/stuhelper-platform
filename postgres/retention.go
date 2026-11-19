package postgres

import (
	"context"
	"database/sql"
	"sync"
	"time"

	e "github.com/StuHelper/stuhelper-platform/eligibility"
	"go.uber.org/fx"
)

type TableRetention struct {
	ExpiredCount    int64      `json:"expired_count"`
	OldestExpiredAt *time.Time `json:"oldest_expired_at,omitempty"`
}

type RetentionStatus struct {
	EligibilityRequests TableRetention `json:"eligibility_requests"`
	AuditSnapshots      TableRetention `json:"audit_snapshots"`
	AdminRequests       TableRetention `json:"admin_requests"`
	Audit               TableRetention `json:"audit"`
}

func (s RetentionStatus) TotalExpired() int64 {
	return s.EligibilityRequests.ExpiredCount + s.AuditSnapshots.ExpiredCount + s.AdminRequests.ExpiredCount + s.Audit.ExpiredCount
}

// RetentionStatus reports expired backlog counts and earliest expired timestamps
// across all four platform retention tables in a single consistent snapshot.
func (r *Repository) RetentionStatus(ctx context.Context, now time.Time) (RetentionStatus, error) {
	var out RetentionStatus
	if r == nil || r.DB == nil {
		return out, e.ErrUnavailable
	}
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := r.DB.BeginTx(call, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return out, unavailable(err)
	}
	defer tx.Rollback()

	queryOne := func(sqlText string, cutoff time.Time, dst *TableRetention) error {
		var oldest sql.NullTime
		if err := tx.QueryRowContext(call, sqlText, cutoff).Scan(&dst.ExpiredCount, &oldest); err != nil {
			return unavailable(err)
		}
		if oldest.Valid {
			t := oldest.Time.UTC()
			dst.OldestExpiredAt = &t
		}
		return nil
	}
	receiptCutoff := now.UTC().Add(-24 * time.Hour)
	auditCutoff := now.UTC().AddDate(-1, 0, 0)
	if err = queryOne(`SELECT count(*), min(created_at) FROM platform.eligibility_requests WHERE created_at <= $1`, receiptCutoff, &out.EligibilityRequests); err != nil {
		return out, err
	}
	if err = queryOne(`SELECT count(*), min(created_at) FROM platform.audit_snapshots WHERE created_at <= $1`, receiptCutoff, &out.AuditSnapshots); err != nil {
		return out, err
	}
	if err = queryOne(`SELECT count(*), min(created_at) FROM platform.admin_requests WHERE created_at <= $1`, receiptCutoff, &out.AdminRequests); err != nil {
		return out, err
	}
	if err = queryOne(`SELECT count(*), min(created_at) FROM platform.audit WHERE created_at <= $1`, auditCutoff, &out.Audit); err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, unavailable(err)
	}
	return out, nil
}

type RetentionReport struct {
	Deleted int64
	Status  RetentionStatus
	Err     error
}

// RetentionCleaner runs bounded periodic retention cleanup and backlog observation
// within an Fx lifecycle, draining any in-flight pass on shutdown.
type RetentionCleaner struct {
	Repo     *Repository
	Interval time.Duration
	Now      func() time.Time
	Observe  func(RetentionReport)

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func (c *RetentionCleaner) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func (c *RetentionCleaner) runOnce(ctx context.Context) {
	at := c.now()
	deleted, err := c.Repo.Cleanup(ctx, at)
	var status RetentionStatus
	if err == nil {
		status, err = c.Repo.RetentionStatus(ctx, at)
	}
	if c.Observe != nil && ctx.Err() == nil {
		c.Observe(RetentionReport{Deleted: deleted, Status: status, Err: err})
	}
}

func (c *RetentionCleaner) Start(_ context.Context) error {
	if c.Repo == nil || c.Repo.DB == nil {
		return e.ErrUnavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done != nil {
		return e.ErrUnavailable
	}
	interval := c.Interval
	if interval <= 0 {
		interval = time.Minute
	}
	runCtx, stop := context.WithCancel(context.Background())
	finished := make(chan struct{})
	c.cancel = stop
	c.done = finished
	go func() {
		defer close(finished)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				c.runOnce(runCtx)
			}
		}
	}()
	return nil
}

func (c *RetentionCleaner) Stop(ctx context.Context) error {
	c.mu.Lock()
	stop, finished := c.cancel, c.done
	c.cancel, c.done = nil, nil
	c.mu.Unlock()
	if stop == nil {
		return nil
	}
	stop()
	select {
	case <-finished:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *RetentionCleaner) RegisterLifecycle(lc fx.Lifecycle) {
	lc.Append(fx.Hook{
		OnStart: c.Start,
		OnStop:  c.Stop,
	})
}
