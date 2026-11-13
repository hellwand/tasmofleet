package store

import (
	"io/fs"
	"testing"
	"testing/fstest"
	"time"

	"github.com/hellwand/tasmofleet/internal/model"
)

// Why (#603): a job scheduled for the night must still know its start time after TasmoFleet restarts in
// between, or it would start at once (in the day) or never. The start time is fixed at creation: a later
// transition must not move it. not_started must survive too, so the transaction can say that the job ended
// without anything sent to the device.
func TestJobStartAtAndNotStartedRoundTrip(t *testing.T) {
	s, _, _ := newStore(t)
	start := time.Date(2026, 11, 14, 2, 0, 0, 0, time.UTC) // 03:00 in Berlin
	scheduled := job(mustDevice(t, s, "night", "10.0.0.1"))
	scheduled.State, scheduled.StartAt = model.JobScheduled, start
	now := job(mustDevice(t, s, "now", "10.0.0.2"))
	_, ids, err := s.CreateBatch(ctx, "alice", []model.Job{scheduled, now})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetJob(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if got.State != model.JobScheduled || !got.StartAt.Equal(start) || got.NotStarted {
		t.Fatalf("scheduled job read back as state %s, start %v, not started %v", got.State, got.StartAt, got.NotStarted)
	}
	if other, err := s.GetJob(ctx, ids[1]); err != nil || !other.StartAt.IsZero() {
		t.Fatalf("a job without a start time read back with start %v (%v): it would wait instead of starting at once", other.StartAt, err)
	}

	// The start passed by more than the window: the job ends without starting.
	got.State, got.NotStarted = model.JobCancelled, true
	got.StartAt = time.Time{} // a transition must not rewrite the start time
	if err := s.SaveTransition(ctx, got, model.JobEvent{JobID: got.ID, From: model.JobScheduled, To: model.JobCancelled, Event: "missed"}); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetJob(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if !got.NotStarted || got.State != model.JobCancelled {
		t.Fatalf("after the transition: state %s, not started %v; want cancelled and not started", got.State, got.NotStarted)
	}
	if !got.StartAt.Equal(start) {
		t.Fatalf("after the transition the start time is %v, want %v (fixed at creation)", got.StartAt, start)
	}
}

// Why (#603): the store is not where a retry drops the start time (the engine does, a retry starts at
// once); CreateRetryBatch stores what it is given, like CreateBatch, so the two never differ.
func TestCreateRetryBatchStoresStartAt(t *testing.T) {
	s, _, _ := newStore(t)
	first := job(mustDevice(t, s, "a", "10.0.0.1"))
	first.State = model.JobFailed
	bid, _, err := s.CreateBatch(ctx, "alice", []model.Job{first})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 15, 2, 0, 0, 0, time.UTC)
	retry := job(first.DeviceID)
	retry.State, retry.StartAt = model.JobScheduled, start
	_, ids, err := s.CreateRetryBatch(ctx, "alice", bid, []model.Job{retry})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetJob(ctx, ids[0]); err != nil || !got.StartAt.Equal(start) {
		t.Fatalf("retry batch job start %v (%v), want %v", got.StartAt, err, start)
	}
}

// Why (#603): the waiting jobs are found in the store (all of them after a restart, and per person for the
// pages), so the filters must select exactly the jobs with a start time, and exactly one person's jobs.
func TestListJobsFiltersScheduledAndCreatedBy(t *testing.T) {
	s, _, _ := newStore(t)
	start := time.Date(2026, 11, 14, 2, 0, 0, 0, time.UTC)
	mk := func(name, addr string, at time.Time) model.Job {
		j := job(mustDevice(t, s, name, addr))
		if !at.IsZero() {
			j.State, j.StartAt = model.JobScheduled, at
		}
		return j
	}
	_, alice, err := s.CreateBatch(ctx, "alice", []model.Job{mk("a1", "10.0.0.1", start), mk("a2", "10.0.0.2", time.Time{})})
	if err != nil {
		t.Fatal(err)
	}
	_, bob, err := s.CreateBatch(ctx, "bob", []model.Job{mk("b1", "10.0.0.3", start)})
	if err != nil {
		t.Fatal(err)
	}
	ids := func(f JobFilter) []model.JobID {
		t.Helper()
		js, err := s.ListJobs(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		var out []model.JobID
		for _, j := range js {
			out = append(out, j.ID)
		}
		return out
	}
	for _, c := range []struct {
		name string
		f    JobFilter
		want []model.JobID
	}{
		{"scheduled", JobFilter{Scheduled: true}, []model.JobID{bob[0], alice[0]}},
		{"by alice", JobFilter{CreatedBy: "alice"}, []model.JobID{alice[1], alice[0]}},
		{"scheduled by alice", JobFilter{CreatedBy: "alice", Scheduled: true}, []model.JobID{alice[0]}},
		{"by nobody known", JobFilter{CreatedBy: "carol"}, nil},
		{"no filter", JobFilter{}, []model.JobID{bob[0], alice[1], alice[0]}},
	} {
		got := ids(c.f)
		if len(got) != len(c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: %v, want %v", c.name, got, c.want)
				break
			}
		}
	}
}

// Why (#603): live databases upgrade with finished and waiting jobs in them. Every job made before 0048
// started at once, so it must read as not scheduled and not "ended without starting"; otherwise an
// upgrade could make queued jobs wait, or relabel old transactions.
func TestMigration0048OldJobsReadAsUnscheduled(t *testing.T) {
	db, path := rawDB(t)
	upTo := fstest.MapFS{}
	entries, err := fs.ReadDir(Migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() >= "0048" {
			continue
		}
		b, err := fs.ReadFile(Migrations, "migrations/"+e.Name())
		if err != nil {
			t.Fatal(err)
		}
		upTo["migrations/"+e.Name()] = &fstest.MapFile{Data: b}
	}
	if err := migrate(db, path, upTo); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO devices (name, address, mac, topic, created_at, updated_at) VALUES ('old','10.0.0.1','aa:bb:cc:00:00:09','t',0,0)`,
		`INSERT INTO devices (name, address, mac, topic, created_at, updated_at) VALUES ('older','10.0.0.2','aa:bb:cc:00:00:0a','u',0,0)`,
		`INSERT INTO ota_batches (id, created_by, created_at, retry_of) VALUES (1,'u',0,0)`,
		`INSERT INTO ota_jobs (batch_id, device_id, state, stage, plan, policy, target_release, target_variant, identity_before, created_by, created_at, updated_at) VALUES (1,1,'queued','final','direct','only-newer','16.0.0','tasmota','{}','u',0,0)`,
		`INSERT INTO ota_jobs (batch_id, device_id, state, stage, plan, policy, target_release, target_variant, identity_before, created_by, created_at, updated_at) VALUES (1,2,'cancelled','final','direct','only-newer','16.0.0','tasmota','{}','u',0,0)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrate(db, path, Migrations); err != nil {
		t.Fatalf("0047 -> current: %v", err)
	}
	s := &SQLite{db: db}
	for _, id := range []model.JobID{1, 2} {
		j, err := s.GetJob(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !j.StartAt.IsZero() || j.NotStarted {
			t.Errorf("job %d from before 0048: start %v, not started %v; want zero and false", id, j.StartAt, j.NotStarted)
		}
	}
	if js, err := s.ListJobs(ctx, JobFilter{Scheduled: true}); err != nil || len(js) != 0 {
		t.Fatalf("jobs from before 0048 listed as scheduled: %d (%v)", len(js), err)
	}
}
