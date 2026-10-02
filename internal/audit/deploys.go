package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/audemed44/hoist/internal/jobs"
)

// DeployRecord is what a finished deploy left running, kept so the stack can be
// rolled back to it.
type DeployRecord struct {
	Job  string    `json:"job"`
	Time time.Time `json:"time"`
	// Finished is when the deploy ended; it counts as good once the stack
	// has run that long without trouble.
	Finished time.Time `json:"finished"`
	Stack    string    `json:"stack"`
	// Services the deploy was limited to; Images covers the whole stack.
	Services []string `json:"services"`
	Trigger  string   `json:"trigger"`
	Commit   string   `json:"commit,omitempty"`
	Dirty    bool     `json:"dirty,omitempty"`
	Result   string   `json:"result"`
	// Rollback is the deploy this one went back to.
	Rollback string `json:"rollback,omitempty"`
	Pinned   bool   `json:"pinned,omitempty"`
	// GoodAt is when its containers had run long enough, without restarts
	// or failing healthchecks, to count as a known-good state.
	GoodAt *time.Time   `json:"good_at,omitempty"`
	Images []jobs.Image `json:"images"`
}

// keepDeploys is how many deploy records stay in the database.
const keepDeploys = 2000

const deploysSchema = `
CREATE TABLE IF NOT EXISTS deploys (
	job      TEXT PRIMARY KEY,
	time     INTEGER NOT NULL,
	finished INTEGER NOT NULL,
	stack    TEXT NOT NULL,
	services TEXT NOT NULL DEFAULT '',
	trigger  TEXT NOT NULL,
	git      TEXT NOT NULL DEFAULT '',
	dirty    INTEGER NOT NULL DEFAULT 0,
	result   TEXT NOT NULL,
	rollback TEXT NOT NULL DEFAULT '',
	pinned   INTEGER NOT NULL DEFAULT 0,
	good_at  INTEGER,
	images   TEXT NOT NULL DEFAULT '[]'
);
CREATE INDEX IF NOT EXISTS deploys_stack ON deploys (stack, time);
`

// RecordDeploy keeps a finished job's images.
func (l *Log) RecordDeploy(ctx context.Context, j *jobs.Job) error {
	if j.Finished == nil {
		return errors.New("the deploy hasn't finished")
	}
	images := j.Images
	if images == nil {
		images = []jobs.Image{}
	}
	data, err := json.Marshal(images)
	if err != nil {
		return err
	}
	result := OK
	if j.State == jobs.Failed {
		result = Failed
	}
	_, err = l.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO deploys (job, time, finished, stack, services, trigger, git, dirty, result, rollback, pinned, images)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.ID, j.Started.UnixMilli(), j.Finished.UnixMilli(), j.Stack, strings.Join(j.Services, ","), j.Trigger,
		j.Commit, j.Dirty, result, j.Rollback, j.Pinned, string(data))
	if err != nil {
		return err
	}
	_, err = l.db.ExecContext(ctx,
		`DELETE FROM deploys WHERE job IN (SELECT job FROM deploys ORDER BY time DESC LIMIT -1 OFFSET ?)`, keepDeploys)
	return err
}

// MarkGood records that a deploy's containers ran without trouble.
func (l *Log) MarkGood(ctx context.Context, job string, at time.Time) error {
	_, err := l.db.ExecContext(ctx, `UPDATE deploys SET good_at = ? WHERE job = ? AND good_at IS NULL`, at.UnixMilli(), job)
	return err
}

const deployColumns = `job, time, finished, stack, services, trigger, git, dirty, result, rollback, pinned, good_at, images`

// Deploys lists a stack's deploy records, newest first.
func (l *Log) Deploys(ctx context.Context, stack string, limit int) ([]DeployRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := l.db.QueryContext(ctx,
		`SELECT `+deployColumns+` FROM deploys WHERE stack = ? ORDER BY time DESC LIMIT ?`, stack, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DeployRecord{}
	for rows.Next() {
		d, err := scanDeploy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ErrNoDeploy means there's no record of that deploy.
var ErrNoDeploy = errors.New("no record of that deploy")

// DeployOf returns one deploy's record.
func (l *Log) DeployOf(ctx context.Context, job string) (DeployRecord, error) {
	row := l.db.QueryRowContext(ctx, `SELECT `+deployColumns+` FROM deploys WHERE job = ?`, job)
	d, err := scanDeploy(row)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNoDeploy
	}
	return d, err
}

func scanDeploy(row interface{ Scan(...any) error }) (DeployRecord, error) {
	var d DeployRecord
	var start, finished int64
	var good sql.NullInt64
	var services, images string
	if err := row.Scan(&d.Job, &start, &finished, &d.Stack, &services, &d.Trigger, &d.Commit, &d.Dirty,
		&d.Result, &d.Rollback, &d.Pinned, &good, &images); err != nil {
		return d, err
	}
	d.Time, d.Finished = time.UnixMilli(start).UTC(), time.UnixMilli(finished).UTC()
	if good.Valid {
		t := time.UnixMilli(good.Int64).UTC()
		d.GoodAt = &t
	}
	d.Services = []string{}
	if services != "" {
		d.Services = strings.Split(services, ",")
	}
	if err := json.Unmarshal([]byte(images), &d.Images); err != nil || d.Images == nil {
		d.Images = []jobs.Image{}
	}
	return d, nil
}
