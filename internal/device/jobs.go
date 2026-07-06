package device

import (
	"fmt"
	"sync/atomic"
	"time"
)

type JobState string

const (
	JobRunning   JobState = "running"
	JobPaused    JobState = "paused"
	JobSucceeded JobState = "succeeded"
	JobFailed    JobState = "failed"
	JobCancelled JobState = "cancelled"
)

const historyLimit = 8

// Job is the wire shape of the shared job model (JSON_PROTOCOL.md §2).
type Job struct {
	ID         string    `json:"job_id"`
	State      JobState  `json:"state"`
	Progress   float64   `json:"progress"`
	EstimatedS float64   `json:"estimated_duration_s"`
	ElapsedS   float64   `json:"elapsed_s"`
	Result     any       `json:"result"`
	Error      *CmdError `json:"error"`
	Kind       string    `json:"-"` // driver bookkeeping, not on the wire
}

type jobRec struct {
	id            string
	kind          string
	state         JobState
	estimate      time.Duration
	elapsed       time.Duration // accumulated run time, excluding pauses
	runningSince  time.Time     // zero while paused or terminal
	finalProgress float64
	result        any
	err           *CmdError
}

func (r *jobRec) elapsedAt(now time.Time) time.Duration {
	e := r.elapsed
	if !r.runningSince.IsZero() {
		e += now.Sub(r.runningSince)
	}
	return e
}

// Jobs implements the job model for one session: at most one active job,
// history ring of the last 8 completed. Loop-only — not goroutine-safe.
type Jobs struct {
	clock   Clock
	seq     int
	active  *jobRec
	history []*jobRec // newest first

	// hasActive mirrors active != nil for cross-goroutine reads (the API's
	// discover-conflict check); every other method stays loop-only.
	hasActive atomic.Bool
}

func NewJobs(c Clock) *Jobs { return &Jobs{clock: c} }

// Start begins a job; CodeBusy if one is already active.
func (j *Jobs) Start(kind string, estimate time.Duration) (Job, *CmdError) {
	if j.active != nil {
		return Job{}, ErrBusy("a job is already running",
			map[string]any{"job_id": j.active.id})
	}
	j.seq++
	j.active = &jobRec{
		id:           fmt.Sprintf("j-%d", j.seq),
		kind:         kind,
		state:        JobRunning,
		estimate:     estimate,
		runningSince: j.clock.Now(),
	}
	j.hasActive.Store(true)
	return j.snapshot(j.active), nil
}

func (j *Jobs) Active() *Job {
	if j.active == nil {
		return nil
	}
	job := j.snapshot(j.active)
	return &job
}

// HasActive reports whether a job is running or paused. Unlike every other
// Jobs method it is safe to call from any goroutine.
func (j *Jobs) HasActive() bool { return j.hasActive.Load() }

func (j *Jobs) ActiveKind() string {
	if j.active == nil {
		return ""
	}
	return j.active.kind
}

func (j *Jobs) Get(id string) *Job {
	if j.active != nil && j.active.id == id {
		job := j.snapshot(j.active)
		return &job
	}
	for _, r := range j.history {
		if r.id == id {
			job := j.snapshot(r)
			return &job
		}
	}
	return nil
}

func (j *Jobs) Complete(result any) *Job {
	return j.finish(JobSucceeded, result, nil, 1.0)
}

func (j *Jobs) Fail(e *CmdError) *Job {
	return j.finish(JobFailed, nil, e, j.currentProgress())
}

func (j *Jobs) Cancel() *Job {
	return j.finish(JobCancelled, nil, nil, j.currentProgress())
}

// Freeze pauses the job clock (pump pause semantics). No-op unless running.
func (j *Jobs) Freeze() {
	r := j.active
	if r == nil || r.state != JobRunning {
		return
	}
	r.elapsed = r.elapsedAt(j.clock.Now())
	r.runningSince = time.Time{}
	r.state = JobPaused
}

func (j *Jobs) Unfreeze() {
	r := j.active
	if r == nil || r.state != JobPaused {
		return
	}
	r.runningSince = j.clock.Now()
	r.state = JobRunning
}

func (j *Jobs) finish(state JobState, result any, e *CmdError, progress float64) *Job {
	if j.active == nil {
		return nil
	}
	r := j.active
	r.elapsed = r.elapsedAt(j.clock.Now())
	r.runningSince = time.Time{}
	r.state = state
	r.result = result
	r.err = e
	r.finalProgress = progress
	j.active = nil
	j.hasActive.Store(false)
	j.history = append([]*jobRec{r}, j.history...)
	if len(j.history) > historyLimit {
		j.history = j.history[:historyLimit]
	}
	job := j.snapshot(r)
	return &job
}

func (j *Jobs) currentProgress() float64 {
	if j.active == nil {
		return 0
	}
	return clampProgress(j.active.elapsedAt(j.clock.Now()), j.active.estimate)
}

func (j *Jobs) snapshot(r *jobRec) Job {
	job := Job{
		ID:         r.id,
		State:      r.state,
		EstimatedS: r.estimate.Seconds(),
		ElapsedS:   r.elapsedAt(j.clock.Now()).Seconds(),
		Result:     r.result,
		Error:      r.err,
		Kind:       r.kind,
	}
	switch r.state {
	case JobRunning, JobPaused:
		job.Progress = clampProgress(r.elapsedAt(j.clock.Now()), r.estimate)
	default:
		job.Progress = r.finalProgress
	}
	return job
}

// clampProgress keeps clock-simulated progress strictly below 1.0: only a
// verified completion may report 1.0 (spec §2.2).
func clampProgress(elapsed, estimate time.Duration) float64 {
	if estimate <= 0 {
		return 0
	}
	p := float64(elapsed) / float64(estimate)
	if p > 0.99 {
		p = 0.99
	}
	if p < 0 {
		p = 0
	}
	return p
}
