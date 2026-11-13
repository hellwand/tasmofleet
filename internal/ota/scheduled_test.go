package ota

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hellwand/tasmofleet/internal/clock"
	"github.com/hellwand/tasmofleet/internal/fwselect"
	"github.com/hellwand/tasmofleet/internal/model"
	"github.com/hellwand/tasmofleet/internal/store"
)

// Scheduled starts (#603). The rules the person is promised in the preview: the update starts at its time;
// if TasmoFleet was down then, it starts as soon as TasmoFleet is back, but only within 60 minutes; later it
// is not started at all and never starts later; it can be cancelled until it starts; and the devices are
// checked again when it starts (Guard, the device lock, a firmware update still running, the OTA exclusion).

// manualClock moves only when the test moves it; After fires once the time reaches it. It lets a test
// look at an engine that waits for a start time without the time passing.
type manualClock struct {
	mu    sync.Mutex
	now   time.Time
	waits []manualWait
}

type manualWait struct {
	at time.Time
	ch chan time.Time
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.waits = append(c.waits, manualWait{at: c.now.Add(d), ch: ch})
	return ch
}

// pending is the time of every timer that has not fired.
func (c *manualClock) pending() []time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]time.Time, 0, len(c.waits))
	for _, w := range c.waits {
		out = append(out, w.at)
	}
	return out
}

// set moves the time to t and fires every timer due by then.
func (c *manualClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
	keep := c.waits[:0]
	for _, w := range c.waits {
		if w.at.After(t) {
			keep = append(keep, w)
			continue
		}
		w.ch <- t
	}
	c.waits = keep
}

// waitFor polls cond for at most five seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func (h *harness) engineWith(clk clock.Clock, check func(context.Context, model.DeviceID) (func(), error)) *Engine {
	e := NewEngine(Options{Machine: testCfg(), MaxParallel: 2, HoldFlashing: h.hold, StartCheck: check}, h.st, h.st, h.api, h.fw, h.backups, clk, nil)
	e.sel = h.sel
	return e
}

// startEngine runs e until the returned stop is called; stop waits for Run to return.
func startEngine(t *testing.T, e *Engine) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	return func() {
		t.Helper()
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not return after its context was cancelled")
		}
	}
}

func hasWorker(e *Engine, id model.JobID) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.workers[id]
	return ok
}

// since returns the log entries after the first n.
func since(h *harness, n int) []string { return h.rec.entries()[n:] }

// deviceCalls are the entries of log that reach a device or change something for it.
func deviceCalls(log []string) []string {
	var out []string
	for _, e := range log {
		switch strings.Fields(e)[0] {
		case "probe_identity", "probe", "set_ota_url", "upgrade", "backup", "guard":
			out = append(out, e)
		}
	}
	return out
}

func eventsOf(t *testing.T, h *harness, id model.JobID) []model.JobEvent {
	t.Helper()
	evs, err := h.st.JobEvents(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

// Why: a scheduled job waits for its time, also across a restart of TasmoFleet. Before the start the
// engine must not give it a worker (Run resumes every job that is not queued) and must not touch the device;
// it only waits, through its clock. At the start the job is queued (event start_due) and runs.
func TestScheduledJobWaitsAcrossARestartAndStartsAtItsTime(t *testing.T) {
	h := newHarness(t, 2, plugDevice())
	start := t0.Add(2 * time.Hour)
	id := submitOne(t, h, Request{DeviceID: 1, Release: "16.0.0", StartAt: start})
	planned := len(h.rec.entries())
	mc := &manualClock{now: t0}

	for run := 1; run <= 2; run++ { // the second run is TasmoFleet after a restart
		e := h.engineWith(mc, nil)
		stop := startEngine(t, e)
		waitFor(t, "the engine to wait for the start", func() bool { return len(mc.pending()) > 0 })
		for _, at := range mc.pending() {
			if at.After(start) {
				t.Fatalf("run %d: the engine wakes at %v, after the start %v", run, at, start)
			}
		}
		mc.set(mc.Now().Add(scheduledRecheck)) // time passes, still before the start
		waitFor(t, "the engine to look again", func() bool { return len(mc.pending()) > 0 })
		if hasWorker(e, id) {
			t.Fatalf("run %d: the scheduled job got a worker before its start", run)
		}
		stop()
		if j := h.job(t, id); j.State != model.JobScheduled {
			t.Fatalf("run %d: job is %s before its start, want scheduled", run, j.State)
		}
		if calls := deviceCalls(since(h, planned)); len(calls) != 0 {
			t.Fatalf("run %d: the device was reached before the start: %v", run, calls)
		}
		if evs := eventsOf(t, h, id); len(evs) != 0 {
			t.Fatalf("run %d: transitions before the start: %+v", run, evs)
		}
	}

	e := h.engineWith(mc, nil)
	stop := startEngine(t, e)
	defer stop()
	waitFor(t, "the engine to wait for the start", func() bool { return len(mc.pending()) > 0 })
	mc.set(start)
	waitFor(t, "the job to start", func() bool { return h.rec.count("probe_identity") > 1 })
	evs := eventsOf(t, h, id)
	if len(evs) == 0 || evs[0].From != model.JobScheduled || evs[0].To != model.JobQueued || evs[0].Event != string(EvStartDue) ||
		evs[0].Detail != "planned for "+WhenText(start)+", started 0s after it" || !evs[0].At.Equal(start) {
		t.Fatalf("first transition %+v, want scheduled -> queued (start_due) at the start", evs)
	}
}

// Why: if TasmoFleet was down at the start, the update starts late only within the fixed 60 minutes (the
// rule the preview states). 59 minutes late it runs, with every step of a normal update; 61 minutes late
// it is not started: the job ends cancelled and NotStarted with the mockup's text, and nothing reaches the
// device, then or later.
func TestScheduledJobLateStartWindow(t *testing.T) {
	for _, c := range []struct {
		name string
		late time.Duration
		want model.JobState
	}{
		{"59 min late starts", 59 * time.Minute, model.JobSucceeded},
		{"61 min late is not started", 61 * time.Minute, model.JobCancelled},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, 2, plugDevice())
			start := t0.Add(time.Minute)
			id := submitOne(t, h, Request{DeviceID: 1, Release: "16.0.0", StartAt: start})
			planned := len(h.rec.entries())
			back := start.Add(c.late)
			h.clk.now = back // TasmoFleet was down from before the start until now
			h.run(t, h.e)

			j := h.job(t, id)
			if j.State != c.want {
				t.Fatalf("job %s (%q), want %s\n%v", j.State, j.LastError, c.want, h.rec.entries())
			}
			evs := eventsOf(t, h, id)
			if c.want == model.JobSucceeded {
				if evs[0].Event != string(EvStartDue) || evs[0].Detail != "planned for "+WhenText(start)+", started 59m0s after it" {
					t.Fatalf("first transition %+v, want start_due with the lateness", evs[0])
				}
				if h.rec.count("upgrade") == 0 || j.NotStarted {
					t.Fatalf("the late start did not update the device (not started %v)", j.NotStarted)
				}
				return
			}
			if !j.NotStarted || j.LastError != NotStartedText(start, back) {
				t.Fatalf("not started %v, LastError %q; want true and %q", j.NotStarted, j.LastError, NotStartedText(start, back))
			}
			if len(evs) != 1 || evs[0].Event != string(EvNotStarted) || evs[0].From != model.JobScheduled || evs[0].To != model.JobCancelled {
				t.Fatalf("transitions %+v, want the one not_started", evs)
			}
			if calls := deviceCalls(since(h, planned)); len(calls) != 0 {
				t.Fatalf("a job past its window reached the device: %v", calls)
			}
			// Never later: another run of the engine leaves it as it is.
			h.run(t, h.newEngine(2))
			if got := eventsOf(t, h, id); len(got) != 1 {
				t.Fatalf("the not started job moved again: %+v", got)
			}
		})
	}
}

// Why (CLAUDE.md safety rule 1): the plan was checked when it was made, but the device or the firmware
// can change until the start. Guard runs again at the start; when it refuses, no OtaUrl, no Upgrade and
// no backup is sent.
func TestScheduledStartRunsGuardAgain(t *testing.T) {
	h := newHarness(t, 2, plugDevice())
	start := t0.Add(time.Minute)
	id := submitOne(t, h, Request{DeviceID: 1, Release: "16.0.0", StartAt: start})
	planned := len(h.rec.entries())
	h.sel.refuse = true // e.g. the device was replaced by another chip since the plan
	h.clk.now = start
	h.run(t, h.e)

	j := h.job(t, id)
	if j.State != model.JobFailed || !strings.Contains(j.LastError, fwselect.ErrFamilyMismatch.Error()) {
		t.Fatalf("job %s %q, want failed by Guard", j.State, j.LastError)
	}
	log := since(h, planned)
	if h.rec.count("guard") <= countPrefix(h.rec.entries()[:planned], "guard") {
		t.Fatalf("Guard did not run at the start: %v", log)
	}
	for _, e := range log {
		if f := strings.Fields(e)[0]; f == "set_ota_url" || f == "upgrade" || f == "backup" {
			t.Fatalf("%q sent although Guard refused at the start: %v", e, log)
		}
	}
	evs := eventsOf(t, h, id)
	if evs[0].Event != string(EvStartDue) || evs[len(evs)-1].Event != string(EvGuardRefused) {
		t.Fatalf("transitions %+v, want start_due ... guard_refused", evs)
	}
}

func countPrefix(log []string, prefix string) int {
	n := 0
	for _, e := range log {
		if strings.HasPrefix(e, prefix) {
			n++
		}
	}
	return n
}

// Why: at the start the device must pass what a person's Submit checks in the application (the device lock,
// a firmware update typed in the console that is still running). A device that is busy at 03:00 (a short
// read, a person's command) is tried again inside the window, and the lock is held until the job is stored
// as queued, so no console command can slip in between. A device that stays busy for the whole window is not
// updated: the job fails with the reason, and nothing was sent to it.
func TestScheduledStartCheckIsRetriedInsideTheWindow(t *testing.T) {
	t.Run("busy at the start, free 30 s later", func(t *testing.T) {
		h := newHarness(t, 2, plugDevice())
		start := t0.Add(time.Minute)
		id := submitOne(t, h, Request{DeviceID: 1, Release: "16.0.0", StartAt: start})
		var calls []time.Time
		check := func(_ context.Context, dev model.DeviceID) (func(), error) {
			calls = append(calls, h.clk.Now())
			h.rec.add("check %d", dev)
			if len(calls) == 1 {
				return nil, errors.New("another operation on this device is running (console)")
			}
			return func() { h.rec.add("release %d", dev) }, nil
		}
		h.clk.now = start
		h.run(t, h.engineWith(h.clk, check))

		if j := h.job(t, id); j.State != model.JobSucceeded {
			t.Fatalf("job %s %q, want succeeded after the second try", j.State, j.LastError)
		}
		if len(calls) != 2 || calls[1].Sub(calls[0]) != startRetry {
			t.Fatalf("start checks at %v, want two, %v apart", calls, startRetry)
		}
		log := h.rec.entries()
		at := func(e string) int {
			for i, x := range log {
				if x == e {
					return i
				}
			}
			t.Fatalf("%q not in %v", e, log)
			return -1
		}
		if at("check 1") > at("save 1 queued") || at("save 1 queued") > at("release 1") {
			t.Fatalf("the device lock must be held until the queued state is stored: %v", log)
		}
		if evs := eventsOf(t, h, id); evs[0].Detail != "planned for "+WhenText(start)+", started 30s after it" {
			t.Fatalf("start_due detail %q", evs[0].Detail)
		}
	})
	t.Run("busy for the whole window", func(t *testing.T) {
		h := newHarness(t, 2, plugDevice())
		start := t0.Add(time.Minute)
		id := submitOne(t, h, Request{DeviceID: 1, Release: "16.0.0", StartAt: start})
		planned := len(h.rec.entries())
		n := 0
		check := func(context.Context, model.DeviceID) (func(), error) {
			n++
			return nil, errors.New("a firmware update typed in the console is probably still running")
		}
		h.clk.now = start
		h.run(t, h.engineWith(h.clk, check))

		j := h.job(t, id)
		if j.State != model.JobFailed || j.NotStarted || !strings.Contains(j.LastError, "typed in the console") || !strings.Contains(j.LastError, "not started") {
			t.Fatalf("job %s (not started %v) %q, want failed with the reason", j.State, j.NotStarted, j.LastError)
		}
		if want := int(60*time.Minute/startRetry) + 1; n != want {
			t.Fatalf("start checked %d times, want %d (every %v for 60 minutes)", n, want, startRetry)
		}
		if calls := deviceCalls(since(h, planned)); len(calls) != 0 {
			t.Fatalf("a device that was never free was reached: %v", calls)
		}
	})
}

// Why: excluding a device from OTA is how a person keeps updates off it. One excluded after the plan is not
// updated at the start, and nothing is sent to it.
func TestScheduledStartRefusesADeviceExcludedSinceThePlan(t *testing.T) {
	h := newHarness(t, 2, plugDevice())
	start := t0.Add(time.Minute)
	id := submitOne(t, h, Request{DeviceID: 1, Release: "16.0.0", StartAt: start})
	planned := len(h.rec.entries())
	h.st.devices[1] = withExcluded(h.st.devices[1])
	h.clk.now = start
	h.run(t, h.e)

	if j := h.job(t, id); j.State != model.JobFailed || !strings.Contains(j.LastError, "excluded from OTA") {
		t.Fatalf("job %s %q, want failed: excluded from OTA", j.State, j.LastError)
	}
	if calls := deviceCalls(since(h, planned)); len(calls) != 0 {
		t.Fatalf("an excluded device was reached: %v", calls)
	}
}

// Why: a plan can be cancelled until it starts; it then ends at once (not "not started": the person chose
// it), never starts later, and frees the device.
func TestCancelScheduledJobBeforeItsStart(t *testing.T) {
	h := newHarness(t, 2, plugDevice(), plugDevice())
	start := t0.Add(time.Hour)
	one := submitOne(t, h, Request{DeviceID: 1, Release: "16.0.0", StartAt: start})
	two := submitOne(t, h, Request{DeviceID: 2, Release: "16.0.0", StartAt: start})
	planned := len(h.rec.entries())
	if err := h.e.Cancel(context.Background(), "alice", one); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if err := h.e.CancelBatch(context.Background(), "alice", h.job(t, two).BatchID); err != nil {
		t.Fatalf("CancelBatch: %v", err)
	}
	for _, id := range []model.JobID{one, two} {
		j := h.job(t, id)
		evs := eventsOf(t, h, id)
		if j.State != model.JobCancelled || j.NotStarted || len(evs) != 1 || evs[0].From != model.JobScheduled || evs[0].Event != string(EvCancel) {
			t.Fatalf("job %d: %s (not started %v), events %+v; want cancelled from scheduled by the cancel", id, j.State, j.NotStarted, evs)
		}
	}
	h.clk.now = start.Add(time.Minute)
	h.run(t, h.e)
	if calls := deviceCalls(since(h, planned)); len(calls) != 0 {
		t.Fatalf("a cancelled plan reached the device: %v", calls)
	}
	if _, err := h.e.Submit(context.Background(), "alice", []Request{{DeviceID: 1, Release: "16.0.0"}}); err != nil {
		t.Fatalf("the device is still held after its plan was cancelled: %v", err)
	}
}

// Why (run with -race): a person may press Cancel at the moment the start comes. Either the cancel wins and
// nothing reaches the device, or the start wins and the cancel reaches the running job; the job is never
// both started and cancelled from scheduled, and never written off as not started.
func TestScheduledStartAndCancelRace(t *testing.T) {
	outcomes := map[string]int{}
	defer func() { t.Logf("outcomes: %v", outcomes) }()
	for i := 0; i < 50; i++ {
		h := newHarness(t, 2, plugDevice())
		start := t0.Add(time.Minute)
		id := submitOne(t, h, Request{DeviceID: 1, Release: "16.0.0", StartAt: start})
		planned := len(h.rec.entries())
		h.clk.now = start
		cerr := make(chan error, 1)
		var e *Engine
		// The cancel is pressed while the start check runs, right before the start is stored.
		e = h.engineWith(h.clk, func(context.Context, model.DeviceID) (func(), error) {
			go func() { cerr <- e.Cancel(context.Background(), "alice", id) }()
			return func() {}, nil
		})
		stop := startEngine(t, e)
		if err := <-cerr; err != nil && !errors.Is(err, ErrNotActive) && !strings.Contains(err.Error(), "busy") {
			t.Fatalf("Cancel: %v", err)
		}
		waitFor(t, "the job to end", func() bool { return h.job(t, id).State.Terminal() })
		stop()

		started, cancelledWaiting := false, false
		for _, ev := range eventsOf(t, h, id) {
			started = started || ev.Event == string(EvStartDue)
			cancelledWaiting = cancelledWaiting || ev.From == model.JobScheduled && ev.To == model.JobCancelled
			if ev.Event == string(EvNotStarted) {
				t.Fatalf("round %d: a job inside its window was written off: %+v", i, ev)
			}
		}
		if started {
			outcomes["started"]++
		} else {
			outcomes["cancelled waiting"]++
		}
		if started == cancelledWaiting {
			t.Fatalf("round %d: started %v, cancelled while waiting %v; want exactly one", i, started, cancelledWaiting)
		}
		if cancelledWaiting {
			if calls := deviceCalls(since(h, planned)); len(calls) != 0 {
				t.Fatalf("round %d: cancelled before the start, but the device was reached: %v", i, calls)
			}
		}
	}
}

// Why: a retry starts at once ("Plan again" is how a new time is chosen); it never copies the start time of
// the job it retries, which has passed and would make the retry not start at all.
func TestRetryOfANotStartedJobStartsAtOnce(t *testing.T) {
	h := newHarness(t, 2, plugDevice())
	start := t0.Add(time.Minute)
	id := submitOne(t, h, Request{DeviceID: 1, Release: "16.0.0", StartAt: start})
	h.clk.now = start.Add(2 * time.Hour)
	h.run(t, h.e)
	if j := h.job(t, id); !j.NotStarted {
		t.Fatalf("job %s, want not started", j.State)
	}
	res, err := h.e.SubmitRetry(context.Background(), "alice", h.job(t, id).BatchID, nil)
	if err != nil {
		t.Fatalf("SubmitRetry: %v", err)
	}
	jobs, _ := h.st.ListJobs(context.Background(), store.JobFilter{BatchID: res.Batch})
	if len(jobs) != 1 || jobs[0].State != model.JobQueued || !jobs[0].StartAt.IsZero() {
		t.Fatalf("retry jobs %+v, want one queued job without a start time", jobs)
	}
	h.run(t, h.e)
	if j := h.job(t, jobs[0].ID); j.State != model.JobSucceeded {
		t.Fatalf("retry %s %q, want succeeded", j.State, j.LastError)
	}
}

// Why: until it starts, a planned update holds its device against every other TasmoFleet job (another
// update, Make compliant, a bulk command), and the refusal says which plan and when, so the person can
// cancel it first instead of reading an unexplained "busy".
func TestScheduledJobHoldsItsDeviceAndTheRefusalNamesThePlan(t *testing.T) {
	h := newHarness(t, 2, plugDevice())
	start := t0.Add(time.Hour)
	id := submitOne(t, h, Request{DeviceID: 1, Release: "16.0.0", StartAt: start})
	want := "has a scheduled update (TX-0001) that starts " + WhenText(start) + "; cancel it first"
	if b := h.job(t, id).BatchID; b != 1 {
		t.Fatalf("batch %d, the expected text assumes 1", b)
	}

	_, err := h.e.Submit(context.Background(), "alice", []Request{{DeviceID: 1, Release: "16.0.0"}})
	var ref *RefusedError
	if !errors.As(err, &ref) || ref.Refused[0].Reason != want {
		t.Fatalf("second update: %v, want refused with %q", err, want)
	}
	_, skipped, err := h.e.SubmitPolicy(context.Background(), "alice", []PolicyRequest{{DeviceID: 1, Commands: []string{"SetOption19 0"}}})
	if !errors.Is(err, ErrNothingToFix) || len(skipped) != 1 || skipped[0].Reason != want {
		t.Fatalf("Make compliant: %v %+v, want skipped with %q", err, skipped, want)
	}
}

// flakyStore fails the reads and writes a due start needs, each once, the way a busy or briefly locked
// database does.
type flakyStore struct {
	*fakeStore
	mu                                        sync.Mutex
	activeCalls                               int
	failActive, failDevice, failGet, failSave bool
}

func (s *flakyStore) take(flag *bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := *flag
	*flag = false
	return f
}

func (s *flakyStore) ActiveJobs(ctx context.Context) ([]model.Job, error) {
	s.mu.Lock()
	s.activeCalls++
	second := s.activeCalls == 2 // the first read is Run's resume, the second the first look at the schedule
	s.mu.Unlock()
	if second && s.take(&s.failActive) {
		return nil, errors.New("database is locked")
	}
	return s.fakeStore.ActiveJobs(ctx)
}

func (s *flakyStore) GetDevice(ctx context.Context, id model.DeviceID) (model.Device, error) {
	if s.take(&s.failDevice) {
		return model.Device{}, errors.New("database is locked")
	}
	return s.fakeStore.GetDevice(ctx, id)
}

func (s *flakyStore) GetJob(ctx context.Context, id model.JobID) (model.Job, error) {
	if s.take(&s.failGet) {
		return model.Job{}, errors.New("database is locked")
	}
	return s.fakeStore.GetJob(ctx, id)
}

func (s *flakyStore) SaveTransition(ctx context.Context, j model.Job, ev model.JobEvent) error {
	if ev.From == model.JobScheduled && s.take(&s.failSave) {
		return errors.New("database is locked")
	}
	return s.fakeStore.SaveTransition(ctx, j, ev)
}

// Why: a scheduled update is started by nobody but the engine. When a database read or write fails at the
// start, the engine must look again soon, inside the window; otherwise the update would silently wait for
// the next unrelated wake-up, and start late or not at all although TasmoFleet was running.
func TestScheduledStartIsRetriedAfterAFailedReadOrWrite(t *testing.T) {
	h := newHarness(t, 2, plugDevice())
	start := t0.Add(time.Minute)
	id := submitOne(t, h, Request{DeviceID: 1, Release: "16.0.0", StartAt: start})
	fs := &flakyStore{fakeStore: h.st, failActive: true, failDevice: true, failGet: true, failSave: true}
	e := NewEngine(Options{Machine: testCfg(), MaxParallel: 2}, fs, fs, h.api, h.fw, h.backups, h.clk, nil)
	e.sel = h.sel
	h.clk.now = start
	h.run(t, e)

	if j := h.job(t, id); j.State != model.JobSucceeded {
		t.Fatalf("job %s %q, want succeeded after the failures passed", j.State, j.LastError)
	}
	// One minute after the failed list, then 30 s after each failed device read, job read and write.
	if evs := eventsOf(t, h, id); evs[0].Detail != "planned for "+WhenText(start)+", started 2m30s after it" {
		t.Fatalf("start_due detail %q", evs[0].Detail)
	}
}

// Why: a device that refused the start is asked again after 30 s, not on every wake of the engine (each
// job that ends, each submit wakes it): the person's console session must not be raced every few
// milliseconds by a waiting update.
func TestARefusedStartIsNotAskedAgainBeforeItsRetry(t *testing.T) {
	h := newHarness(t, 2, plugDevice())
	start := t0.Add(time.Minute)
	submitOne(t, h, Request{DeviceID: 1, Release: "16.0.0", StartAt: start})
	mc := &manualClock{now: start}
	var mu sync.Mutex
	asked := 0
	e := h.engineWith(mc, func(context.Context, model.DeviceID) (func(), error) {
		mu.Lock()
		asked++
		mu.Unlock()
		return nil, errors.New("another operation on this device is running (console)")
	})
	count := func() int { mu.Lock(); defer mu.Unlock(); return asked }
	stop := startEngine(t, e)
	defer stop()
	waitFor(t, "the first start check", func() bool { return count() == 1 && len(mc.pending()) > 0 })
	for i := 0; i < 3; i++ {
		n := len(mc.pending())
		e.signal()
		waitFor(t, "the engine to look again", func() bool { return len(mc.pending()) > n })
	}
	if n := count(); n != 1 {
		t.Fatalf("the device was asked %d times before the retry time", n)
	}
	mc.set(start.Add(startRetry))
	waitFor(t, "the retry", func() bool { return count() == 2 })
}

// Why: a cancel that lands while the start check runs wins: the stored job is read again under the engine
// lock before the start is stored, so a cancelled plan never starts and nothing reaches the device.
func TestCancelDuringTheStartCheckWins(t *testing.T) {
	h := newHarness(t, 2, plugDevice())
	start := t0.Add(time.Minute)
	id := submitOne(t, h, Request{DeviceID: 1, Release: "16.0.0", StartAt: start})
	planned := len(h.rec.entries())
	var e *Engine
	released := false
	e = h.engineWith(h.clk, func(context.Context, model.DeviceID) (func(), error) {
		if err := e.Cancel(context.Background(), "alice", id); err != nil {
			t.Errorf("Cancel: %v", err)
		}
		return func() { released = true }, nil
	})
	h.clk.now = start
	h.run(t, e)

	evs := eventsOf(t, h, id)
	if j := h.job(t, id); j.State != model.JobCancelled || len(evs) != 1 || evs[0].Event != string(EvCancel) {
		t.Fatalf("job %s, events %+v; want only the cancel", j.State, evs)
	}
	if !released {
		t.Fatal("the device lock taken for the start was not given back")
	}
	if calls := deviceCalls(since(h, planned)); len(calls) != 0 {
		t.Fatalf("a plan cancelled during its start check reached the device: %v", calls)
	}
}

// Why: "Retry failed" of a Make compliant transaction is another TasmoFleet job on the device; a planned
// update holds the device against it too, and the refusal names the plan.
func TestPolicyRetryIsRefusedForADeviceWithAScheduledUpdate(t *testing.T) {
	h := newHarness(t, 2, plugDevice())
	fix, _, err := h.e.SubmitPolicy(context.Background(), "alice", []PolicyRequest{{DeviceID: 1, Commands: []string{"SetOption19 0"}}})
	if err != nil {
		t.Fatalf("SubmitPolicy: %v", err)
	}
	h.run(t, h.e) // without a policy applier the job fails
	start := t0.Add(time.Hour)
	id := submitOne(t, h, Request{DeviceID: 1, Release: "16.0.0", StartAt: start})
	_, err = h.e.SubmitRetry(context.Background(), "alice", fix, nil)
	var ref *RefusedError
	want := fmt.Sprintf("has a scheduled update (TX-%04d) that starts %s; cancel it first", h.job(t, id).BatchID, WhenText(start))
	if !errors.As(err, &ref) || len(ref.Refused) != 1 || ref.Refused[0].Reason != want {
		t.Fatalf("retry: %v, want refused with %q", err, want)
	}
}
