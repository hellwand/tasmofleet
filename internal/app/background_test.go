package app

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hellwand/tasmofleet/internal/backup"
	"github.com/hellwand/tasmofleet/internal/health"
	"github.com/hellwand/tasmofleet/internal/metrics"
	"github.com/hellwand/tasmofleet/internal/model"
)

// Why: Tasmota names the only relay of a single-relay device "POWER" and the
// relays of a multi-relay device "POWER1".."POWERn"; the metric must use one
// relay number for both, and never two series for the same relay.
func TestDeviceSamplesRelayNumbersAndUnknown(t *testing.T) {
	devs := []model.Device{{ID: 1, Name: "single"}, {ID: 2, Name: "multi"}, {ID: 3, Name: "silent"}, {ID: 4, Name: "absent"}}
	all := map[model.DeviceID]model.Health{
		1: {State: model.HealthOnline, Power: map[string]string{"POWER": "ON"}},
		2: {State: model.HealthOnline, Power: map[string]string{"POWER": "OFF", "POWER1": "ON", "POWER2": "OFF"}},
		3: {State: model.HealthUnknown},
	}
	tel := map[model.DeviceID]health.Telemetry{1: {SignalDBm: -61, HasSignal: true}}
	got := deviceSamples(devs, all, tel)
	if len(got) != 4 {
		t.Fatalf("samples = %d", len(got))
	}
	if r := got[0].Relays; len(r) != 1 || !r["1"] {
		t.Errorf("single relay = %v", r)
	}
	if r := got[1].Relays; len(r) != 2 || !r["1"] || r["2"] {
		t.Errorf("multi relay = %v (bare POWER must not override POWER1)", r)
	}
	if got[0].SignalDBm == nil || *got[0].SignalDBm != -61 || got[1].SignalDBm != nil {
		t.Errorf("signal mapping wrong: %v %v", got[0].SignalDBm, got[1].SignalDBm)
	}
	if !got[2].Unknown || !got[3].Unknown {
		t.Error("devices without observation must be marked unknown")
	}
}

// Why: POWER999 from a hostile publisher must not become a relay series.
func TestDeviceSamplesDropOutOfRangeRelays(t *testing.T) {
	devs := []model.Device{{ID: 1, Name: "d"}}
	all := map[model.DeviceID]model.Health{1: {State: model.HealthOnline,
		Power: map[string]string{"POWER2": "ON", "POWER999": "ON", "POWER0": "ON"}}}
	got := deviceSamples(devs, all, nil)
	if r := got[0].Relays; len(r) != 1 || !r["2"] {
		t.Fatalf("relays = %v", r)
	}
}

// Why (#145): sensor readings are for the device page only. Exporting them would add metric
// series nobody asked for and change the exposition scrapers already parse; the samples must
// be identical with and without them.
func TestDeviceSamplesIgnoreSensorReadings(t *testing.T) {
	devs := []model.Device{{ID: 1, Name: "weather"}}
	all := map[model.DeviceID]model.Health{1: {State: model.HealthOnline}}
	base := health.Telemetry{SignalDBm: -60, HasSignal: true}
	with := base
	with.Sensors = []health.SensorReading{{Sensor: "BME280", Name: "Temperature", Value: 21.5, Unit: "°C"}}
	a := deviceSamples(devs, all, map[model.DeviceID]health.Telemetry{1: base})
	b := deviceSamples(devs, all, map[model.DeviceID]health.Telemetry{1: with})
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("sensor readings changed the metric samples:\n%+v\n%+v", a, b)
	}
}

type fakeBackupList map[model.DeviceID][]model.Backup

func (f fakeBackupList) List(_ context.Context, id model.DeviceID) ([]model.Backup, error) {
	return f[id], nil
}

// Why (#545): the backup age gauge says how long a device has gone without a
// backup. After "Replace device" the newest file filed under the old device
// can be the replacement hardware's safety backup; the gauge must still show
// the device's own last backup, and a device that only has replacement runs
// has no backup age at all (an absent series, not a fresh one).
func TestBackupSamplesUseTheDevicesOwnNewestRun(t *testing.T) {
	t1 := time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC)
	t2 := t1.Add(24 * time.Hour)
	devs := []model.Device{{ID: 1, Name: "replaced"}, {ID: 2, Name: "plain"}, {ID: 3, Name: "only-spare"}, {ID: 4, Name: "none"}}
	spare := func(at time.Time) model.Backup {
		return model.Backup{Kind: model.BackupConfigDump, TakenAt: at, Trigger: model.TriggerManual, Note: backup.NoteReplacementHardware}
	}
	own := func(at time.Time) model.Backup {
		return model.Backup{Kind: model.BackupConfigDump, TakenAt: at, Trigger: model.TriggerScheduled}
	}
	got := backupSamples(t.Context(), devs, fakeBackupList{
		1: {spare(t2), own(t1)},
		2: {own(t2), own(t1)},
		3: {spare(t2)},
	})
	want := []metrics.BackupSample{{ID: 1, Name: "replaced", Taken: t1}, {ID: 2, Name: "plain", Taken: t2}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("samples = %+v\nwant     %+v", got, want)
	}
}

// Why (#603): tasmofleet_ota_jobs_active is what an alert on "updates at work" reads. A job that waits for
// its scheduled start (maybe for days) does nothing yet; counting it would show work where there is none.
func TestOTAJobsActiveLeavesOutScheduledJobs(t *testing.T) {
	svc := importServices(t)
	ctx := t.Context()
	var jobs []model.Job
	for i, s := range []model.JobState{model.JobScheduled, model.JobQueued, model.JobPreflight} {
		d, err := svc.Store.CreateDevice(ctx, model.Device{Name: "d" + strconv.Itoa(i), Address: "192.0.2." + strconv.Itoa(i+1)})
		if err != nil {
			t.Fatal(err)
		}
		j := model.Job{DeviceID: d, State: s, Stage: model.StageFinal, Policy: model.PolicyOnlyNewer, TargetRelease: "16.1.0"}
		if s == model.JobScheduled {
			j.StartAt = time.Now().Add(time.Hour)
		}
		jobs = append(jobs, j)
	}
	if _, _, err := svc.Store.CreateBatch(ctx, "u", jobs); err != nil {
		t.Fatal(err)
	}
	bm, err := backup.New(backup.Options{DataDir: t.TempDir(), Devices: svc.Store, Store: svc.Store, Client: svc.Client, Log: svc.Log})
	if err != nil {
		t.Fatal(err)
	}
	a := &runtime{svc: svc, cfg: svc.Cfg, log: svc.Log, tracker: health.New(health.Deps{}, health.Options{}), backups: bm}
	a.updateMetrics(ctx)
	if got := scrape(svc.Metrics); !strings.Contains(got, "\ntasmofleet_ota_jobs_active 2\n") {
		t.Fatalf("want 2 jobs at work (queued and preflight, not the scheduled one):\n%s", got)
	}
}
