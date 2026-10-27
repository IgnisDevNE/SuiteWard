package river

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	riverqueue "github.com/riverqueue/river"
)

// Wake is the set of job kinds a run asks the runtime to run soon
// (docs/contracts/runtime.md, "Queues and wake-ups").
type Wake []string

// wakeTimeout bounds the check and the insert of one wake-up, which run even when the job's own context ended.
const wakeTimeout = 10 * time.Second

// waker inserts the wake-ups of the runs that returned them. client is set by NewRuntime before the client starts.
type waker struct {
	client *riverqueue.Client[pgx.Tx]
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// wake inserts one job of each kind unless one is already waiting. A failure is logged with its cause and never
// reaches the run: the periodic run is the fallback.
func (w *waker) wake(ctx context.Context, kinds Wake) {
	ctx = context.WithoutCancel(ctx) // a run that failed late may still have committed work
	for _, kind := range kinds {
		args, ok := wakeable[kind]
		if !ok {
			w.logger.ErrorContext(ctx, "wake-up of a job kind that cannot be woken", "kind", kind)
			continue
		}
		if err := w.insert(ctx, kind, args); err != nil {
			w.logger.WarnContext(ctx, "wake-up failed", "kind", kind, "error", err)
		}
	}
}

// insert queues one job of kind, with one attempt and no unique options, unless one is available or scheduled.
// Two concurrent wakers can both insert; that costs one extra run.
func (w *waker) insert(ctx context.Context, kind string, args riverqueue.JobArgs) error {
	ctx, cancel := context.WithTimeout(ctx, wakeTimeout)
	defer cancel()
	var waiting bool
	if err := w.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM river_job WHERE kind = $1 AND state IN ('available', 'scheduled'))", kind).Scan(&waiting); err != nil || waiting {
		return err
	}
	_, err := w.client.Insert(ctx, args, &riverqueue.InsertOpts{Queue: kind, MaxAttempts: 1})
	return err
}

// Wake queues one job of each kind, as the wake-up of a run does, for a caller that is not a job (a request that committed work): the same
// insert, on the queue of the kind and not while one is waiting, with each failure returned to the caller instead of logged. A kind that a run may
// not wake is an error that names it, and the other kinds are still woken.
func (r *Runtime) Wake(ctx context.Context, kinds Wake) error {
	var errs []error
	for _, kind := range kinds {
		args, ok := wakeable[kind]
		if !ok {
			errs = append(errs, fmt.Errorf("job kind %q cannot be woken", kind))
			continue
		}
		if err := r.waker.insert(ctx, kind, args); err != nil {
			errs = append(errs, fmt.Errorf("wake %s: %w", kind, err))
		}
	}
	return errors.Join(errs...)
}
