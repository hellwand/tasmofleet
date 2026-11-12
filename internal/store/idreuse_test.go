package store

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/hellwand/tasmofleet/internal/model"
)

// historyOf is the audit history the device console and the transaction page show for target.
func historyOf(t *testing.T, s *SQLite, target string) []model.AuditEvent {
	t.Helper()
	evs, err := s.ListAudit(ctx, AuditFilter{Target: target})
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

// Why (#498): the audit log is the record of who did what to which device, and it names the device
// by its id ("device:N"). When the newest device is deleted and its id handed to the next new one,
// the new device inherits the deleted device's console history, old links and notifications open it,
// and the record says that commands were sent to a device that never received them.
func TestDeletedDeviceIDIsNeverHandedOutAgain(t *testing.T) {
	s, _, _ := newStore(t)
	mustDevice(t, s, "kitchen", "10.0.0.1")
	gone := mustDevice(t, s, "garage", "10.0.0.2")
	if err := s.AppendAudit(ctx, model.AuditEvent{Actor: "adam", Action: "device.console", Target: fmt.Sprintf("device:%d", gone), Result: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteDevice(ctx, gone); err != nil {
		t.Fatal(err)
	}
	fresh := mustDevice(t, s, "porch", "10.0.0.3")
	if fresh <= gone {
		t.Fatalf("the new device got id %d after the deleted device %d: ids must only grow", fresh, gone)
	}
	if evs := historyOf(t, s, fmt.Sprintf("device:%d", fresh)); len(evs) != 0 {
		t.Fatalf("the new device shows %d audit rows of another device: %+v", len(evs), evs)
	}
	// A row written with an explicit id outside the store must not make the next insert clash on the key.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO devices (id, name, address, mac, created_at, updated_at) VALUES (?, 'seeded', '10.0.0.40', '', 1, 1)`, fresh+10); err != nil {
		t.Fatal(err)
	}
	if id := mustDevice(t, s, "attic", "10.0.0.4"); id != fresh+11 {
		t.Fatalf("device id %d after a row with id %d was written directly, want %d", id, fresh+10, fresh+11)
	}
}

// Why (#498): a transaction is named "transaction:N" in the audit log, in notification links and on
// the transaction page. Once PruneJobs removed the newest transaction, the next one must not take its
// number and show its start event and history.
func TestPrunedTransactionIDIsNeverHandedOutAgain(t *testing.T) {
	s, _, _ := newStore(t)
	dev := mustDevice(t, s, "kitchen", "10.0.0.1")
	cut := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	batchOf(t, s, model.JobSucceeded, cut.Add(-48*time.Hour), dev)
	gone, _ := batchOf(t, s, model.JobSucceeded, cut.Add(-24*time.Hour), dev)
	if err := s.AppendAudit(ctx, model.AuditEvent{Actor: "adam", Action: "update.start", Target: fmt.Sprintf("transaction:%d", gone), Result: "ok"}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.PruneJobs(ctx, cut); err != nil || n != 2 {
		t.Fatalf("pruned %d jobs (%v), want both old transactions", n, err)
	}
	fresh, _, err := s.CreateBatch(ctx, "adam", []model.Job{job(dev)})
	if err != nil {
		t.Fatal(err)
	}
	if fresh <= gone {
		t.Fatalf("the new transaction got id %d after the pruned transaction %d: ids must only grow", fresh, gone)
	}
	if evs := historyOf(t, s, fmt.Sprintf("transaction:%d", fresh)); len(evs) != 0 {
		t.Fatalf("the new transaction shows %d audit rows of another one: %+v", len(evs), evs)
	}
	// The e2e seed writes transactions with an explicit id; the next one must not clash on the key.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO ota_batches (id, created_by, created_at) VALUES (?, 'seed', 1)`, fresh+10); err != nil {
		t.Fatal(err)
	}
	other := mustDevice(t, s, "garage", "10.0.0.2") // dev still holds the queued job of fresh
	if b, _, err := s.CreateBatch(ctx, "adam", []model.Job{job(other)}); err != nil || b != fresh+11 {
		t.Fatalf("transaction id %d (%v) after a row with id %d was written directly, want %d", b, err, fresh+10, fresh+11)
	}
}

// migrate0047 builds a database as 0.34.x left it (up to 0046), runs seed on it, then the migrations
// that follow, and returns the opened store.
func migrate0047(t *testing.T, seed ...string) *SQLite {
	t.Helper()
	path := filepath.Join(t.TempDir(), "v0340.db")
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() }) //nolint:errcheck // temp DB
	old := fstest.MapFS{}
	entries, err := fs.ReadDir(Migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() >= "0047" {
			continue
		}
		b, err := fs.ReadFile(Migrations, "migrations/"+e.Name())
		if err != nil {
			t.Fatal(err)
		}
		old["migrations/"+e.Name()] = &fstest.MapFile{Data: b}
	}
	if err := migrate(db, path, old); err != nil {
		t.Fatal(err)
	}
	for _, q := range seed {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := migrate(db, path, Migrations); err != nil {
		t.Fatalf("0046 -> current: %v", err)
	}
	return &SQLite{db: db, now: time.Now}
}

// Why (#498): devices and transactions deleted before the upgrade still have their rows in the audit
// log, and nothing else remembers their ids. The first device and the first transaction created after
// the upgrade must not take one of those ids either, so the migration starts above the highest id the
// audit log names ("batch:N" is the old spelling of "transaction:N"). Existing rows keep their ids and
// what refers to them.
func TestMigration0047StartsAboveIDsKnownOnlyFromTheAuditLog(t *testing.T) {
	s := migrate0047(t,
		`INSERT INTO devices (id, name, address, mac, created_at, updated_at) VALUES (1, 'lamp', '10.0.0.1', 'aa:bb:cc:00:00:01', 1, 1)`,
		`INSERT INTO devices (id, name, address, mac, created_at, updated_at) VALUES (2, 'plug', '10.0.0.2', '', 1, 1)`,
		`INSERT INTO device_macs (mac, device_id) VALUES ('aa:bb:cc:00:00:01', 1)`,
		`INSERT INTO ota_batches (id, created_by, created_at) VALUES (3, 'adam', 1)`,
		`INSERT INTO ota_jobs (batch_id, device_id, state, stage, target_release, target_variant, created_by, created_at, updated_at) VALUES (3, 2, 'succeeded', 'final', '16.0.0', 'tasmota', 'adam', 1, 1)`,
		`INSERT INTO audit_log (at, actor, action, target, result) VALUES (1, 'adam', 'device.delete', 'device:7', 'ok')`,
		`INSERT INTO audit_log (at, actor, action, target, result) VALUES (1, 'adam', 'device.console', 'device:abc', 'ok')`,
		`INSERT INTO audit_log (at, actor, action, target, result) VALUES (1, 'adam', 'update.start', 'transaction:9', 'ok')`,
		`INSERT INTO audit_log (at, actor, action, target, result) VALUES (1, 'adam', 'update.start', 'batch:15', 'ok')`,
	)
	if d, err := s.GetDevice(ctx, 1); err != nil || d.Name != "lamp" || len(d.MACs) != 1 {
		t.Fatalf("existing device 1 after the migration: %+v %v", d, err)
	}
	if jobs, err := s.ListJobs(ctx, JobFilter{BatchID: 3}); err != nil || len(jobs) != 1 || jobs[0].DeviceID != 2 {
		t.Fatalf("the existing transaction 3 lost its job: %+v %v", jobs, err)
	}
	if id := mustDevice(t, s, "porch", "10.0.0.3"); id != 8 {
		t.Errorf("first device after the upgrade got id %d, want 8 (above device:7 of the audit log)", id)
	}
	if b, _, err := s.CreateBatch(ctx, "adam", []model.Job{job(1)}); err != nil || b != 16 {
		t.Errorf("first transaction after the upgrade got id %d (%v), want 16 (above batch:15 of the audit log)", b, err)
	}
}
