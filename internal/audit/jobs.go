package audit

import (
	"context"
	"log/slog"
	"time"

	"github.com/audemed44/hoist/internal/jobs"
)

// JobFinished records a deploy's outcome; it's the jobs store's OnFinish.
func (l *Log) JobFinished(j *jobs.Job) {
	result := OK
	if j.State == jobs.Failed {
		result = Failed
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := l.FinishJob(ctx, j.ID, result, j.Result.Summary(), j.Error); err != nil {
		slog.Warn("audit: could not record the deploy's result", "job", j.ID, "err", err)
	}
}
