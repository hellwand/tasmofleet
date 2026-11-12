package ota

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hellwand/tasmofleet/internal/model"
	"github.com/hellwand/tasmofleet/internal/store"
)

// Why (#695): Run's first statement reads the active jobs to resume them. A context cancelled
// before or during that read is a stop, not a failure, exactly like a cancel anywhere else in Run:
// callers that report Run's error (the integration harness, which can cancel an engine right after
// starting it) must not see "ota: resume: context canceled". A real read error while the context is
// still live is a failure and must still reach the caller, or a broken database would let the
// engine stop silently without resuming the devices that are mid-update.

// ctxStore reads active jobs like the SQLite store does (QueryContext): a done context fails the
// read with the context's error. before runs at the start of each read (nil: nothing).
type ctxStore struct {
	*fakeStore
	before func()
}

func (s ctxStore) ActiveJobs(ctx context.Context) ([]model.Job, error) {
	if s.before != nil {
		s.before()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.fakeStore.ActiveJobs(ctx)
}

// brokenActiveJobs fails every active-jobs read with err, whatever the context.
type brokenActiveJobs struct {
	*fakeStore
	err error
}

func (s brokenActiveJobs) ActiveJobs(context.Context) ([]model.Job, error) { return nil, s.err }

func TestRunResumeReadCancelIsAStopButAReadErrorIsNot(t *testing.T) {
	errDisk := errors.New("disk I/O error")
	for _, tc := range []struct {
		name    string
		store   func(s *fakeStore, cancel context.CancelFunc) store.JobStore
		cancel  bool  // cancel the context before Run starts
		wantErr error // nil: Run must return nil
	}{
		{
			name:   "cancelled before the resume read is a clean stop",
			store:  func(s *fakeStore, _ context.CancelFunc) store.JobStore { return ctxStore{fakeStore: s} },
			cancel: true,
		},
		{
			// The race of #695: the harness cancels while the query is still running.
			name: "cancelled during the resume read is a clean stop",
			store: func(s *fakeStore, cancel context.CancelFunc) store.JobStore {
				return ctxStore{fakeStore: s, before: cancel}
			},
		},
		{
			name:    "read error with a live context is reported",
			store:   func(s *fakeStore, _ context.CancelFunc) store.JobStore { return brokenActiveJobs{s, errDisk} },
			wantErr: errDisk,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, 1, plugDevice())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			h.e.jobs = tc.store(h.st, cancel)
			if tc.cancel {
				cancel()
			}
			done := make(chan error, 1)
			go func() { done <- h.e.Run(ctx) }()
			var err error
			select {
			case err = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("Run did not return; a failed resume read with a live context must end Run with that error")
			}
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Run = %v, want nil: a cancelled start is a stop, not a failure", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) || !strings.Contains(err.Error(), "ota: resume") {
				t.Fatalf("Run = %v, want the resume read error %q", err, tc.wantErr)
			}
		})
	}
}
