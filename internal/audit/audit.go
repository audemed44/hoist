// Package audit keeps a log of everything done through Hoist (deploys,
// edits, commits, .env changes, updates, stacks added) in a SQLite database,
// so it can be filtered by stack, action and time.
package audit

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure Go, so the build stays static
)

// Actions.
const (
	Deploy      = "deploy"
	ComposeSave = "compose.save"
	EnvSave     = "env.save"
	GitCommit   = "git.commit"
	GitDiscard  = "git.discard"
	GitPull     = "git.pull"
	GitPush     = "git.push"
	UpdateApply = "update.apply"
	StackAdopt  = "stack.adopt"
	StackCreate = "stack.create"
	// Rollback pins a stack to an earlier deploy; RollbackResume unpins it.
	Rollback       = "rollback.pin"
	RollbackResume = "rollback.resume"
	// The release board's actions on GitHub.
	ReleaseMerge = "release.merge"
	ReleaseRerun = "release.rerun"
	ReleaseShip  = "release.ship"
)

// Results.
const (
	OK      = "ok"
	Failed  = "failed"
	Running = "running"
)

type Event struct {
	ID    int64     `json:"id"`
	Time  time.Time `json:"time"`
	Stack string    `json:"stack"`
	// Services the action was limited to; empty means the whole stack.
	Services []string `json:"services"`
	Action   string   `json:"action"`
	// Trigger is where it came from: ui (a signed-in browser), api (the
	// bearer token), foyer, or auto (an update policy).
	Trigger string `json:"trigger"`
	Detail  string `json:"detail,omitempty"`
	Commit  string `json:"commit,omitempty"`
	Job     string `json:"job,omitempty"`
	Result  string `json:"result"`
	Error   string `json:"error,omitempty"`
}

// keep is how many events stay in the database.
const keep = 20000

type Log struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS events (
	id       INTEGER PRIMARY KEY AUTOINCREMENT,
	time     INTEGER NOT NULL,
	stack    TEXT NOT NULL,
	services TEXT NOT NULL DEFAULT '',
	action   TEXT NOT NULL,
	trigger  TEXT NOT NULL,
	detail   TEXT NOT NULL DEFAULT '',
	git      TEXT NOT NULL DEFAULT '',
	job      TEXT NOT NULL DEFAULT '',
	result   TEXT NOT NULL,
	error    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS events_stack ON events (stack, id);
CREATE INDEX IF NOT EXISTS events_job ON events (job) WHERE job != '';
`

// Open opens (or creates) the database. Both Hoist and its self-deploy
// helper write to it, hence WAL and the busy timeout.
func Open(path string) (*Log, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=cache_size(-512)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection, closed when idle: the log is written a few times a
	// day, and an open SQLite connection holds its page cache.
	db.SetMaxOpenConns(1)
	db.SetConnMaxIdleTime(time.Minute)
	if _, err := db.Exec(schema + deploysSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("audit database: %w", err)
	}
	return &Log{db: db}, nil
}

func (l *Log) Close() error { return l.db.Close() }

// Record adds an event, stamping it with the current time, and returns its ID.
func (l *Log) Record(ctx context.Context, e Event) (int64, error) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	if e.Result == "" {
		e.Result = OK
	}
	res, err := l.db.ExecContext(ctx,
		`INSERT INTO events (time, stack, services, action, trigger, detail, git, job, result, error)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Time.UnixMilli(), e.Stack, strings.Join(e.Services, ","), e.Action, e.Trigger,
		e.Detail, e.Commit, e.Job, e.Result, e.Error)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err == nil && id%100 == 0 {
		_, _ = l.db.ExecContext(ctx, `DELETE FROM events WHERE id <= ?`, id-keep)
	}
	return id, err
}

// FinishJob records how a deploy ended on the events that started it (the
// deploy, and a rollback or update that led to it). Only the deploy's
// detail becomes the job's summary; the others keep theirs.
func (l *Log) FinishJob(ctx context.Context, job, result, detail, errMsg string) error {
	_, err := l.db.ExecContext(ctx,
		`UPDATE events SET result = ?, detail = CASE WHEN ? = '' OR action != ? THEN detail ELSE ? END, error = ?
		 WHERE job = ? AND result = ?`,
		result, detail, Deploy, detail, errMsg, job, Running)
	return err
}

// Query filters the log. Zero values match everything.
type Query struct {
	Stack   string
	Action  string // an action, or a prefix ending in "." (e.g. "git.")
	Trigger string
	Result  string
	Service string
	Since   time.Time
	// Before pages backwards: only events with a smaller ID.
	Before int64
	Limit  int
}

// List returns the newest matching events first.
func (l *Log) List(ctx context.Context, q Query) ([]Event, error) {
	var where []string
	var args []any
	add := func(cond string, arg any) {
		where = append(where, cond)
		args = append(args, arg)
	}
	if q.Stack != "" {
		add("stack = ?", q.Stack)
	}
	if strings.HasSuffix(q.Action, ".") {
		add("action LIKE ? ESCAPE '\\'", escapeLike(q.Action)+"%")
	} else if q.Action != "" {
		add("action = ?", q.Action)
	}
	if q.Trigger != "" {
		add("trigger = ?", q.Trigger)
	}
	if q.Result != "" {
		add("result = ?", q.Result)
	}
	if q.Service != "" {
		add("(',' || services || ',') LIKE ? ESCAPE '\\'", "%,"+escapeLike(q.Service)+",%")
	}
	if !q.Since.IsZero() {
		add("time >= ?", q.Since.UnixMilli())
	}
	if q.Before > 0 {
		add("id < ?", q.Before)
	}
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 100
	}
	query := `SELECT id, time, stack, services, action, trigger, detail, git, job, result, error FROM events`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY id DESC LIMIT ?"
	args = append(args, q.Limit)
	rows, err := l.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var ms int64
		var services string
		if err := rows.Scan(&e.ID, &ms, &e.Stack, &services, &e.Action, &e.Trigger,
			&e.Detail, &e.Commit, &e.Job, &e.Result, &e.Error); err != nil {
			return nil, err
		}
		e.Time = time.UnixMilli(ms).UTC()
		e.Services = []string{}
		if services != "" {
			e.Services = strings.Split(services, ",")
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
