package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/hellwand/tasmofleet/internal/clock"
	"github.com/hellwand/tasmofleet/internal/firmware"
	"github.com/hellwand/tasmofleet/internal/ota"
	"github.com/hellwand/tasmofleet/internal/restore"
	"github.com/hellwand/tasmofleet/internal/settings"
	"github.com/hellwand/tasmofleet/internal/store"
	"github.com/hellwand/tasmofleet/internal/tasmota"
	"github.com/hellwand/tasmofleet/internal/templates"
	"github.com/hellwand/tasmofleet/internal/transport"
)

// heldClient answers like the device behind it, but holds every command from the second one on until
// release is closed: a device that answers a Settings read slowly. held is closed when the read first waits.
type heldClient struct {
	tasmota.Client
	mu      sync.Mutex
	sent    int
	held    chan struct{}
	release chan struct{}
	once    sync.Once
}

func newHeldClient(c tasmota.Client) *heldClient {
	return &heldClient{Client: c, held: make(chan struct{}), release: make(chan struct{})}
}

func (h *heldClient) Command(ctx context.Context, t tasmota.Target, cmnd string) (json.RawMessage, error) {
	h.mu.Lock()
	h.sent++
	wait := h.sent > 1
	h.mu.Unlock()
	if wait {
		h.once.Do(func() { close(h.held) })
		select {
		case <-h.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return h.Client.Command(ctx, t, cmnd)
}

// readRacesWrite starts a Settings read of the stored device, lets it get under way, runs write (another
// writer of the device's settings), lets the read end, and returns what the service remembers afterwards.
// It first runs the same read without any write: the rig must remember what it read, or "nothing is
// remembered" would prove nothing.
func readRacesWrite(t *testing.T, write func(a *runtime, o anotherDevice)) (remembered bool) {
	t.Helper()
	o := newAnotherDevice(t)

	control := settings.New(settings.Options{Devices: o.st, Client: o.cl})
	if _, err := control.Read(t.Context(), o.id); err != nil {
		t.Fatalf("control read: %v", err)
	}
	if _, ok := control.Last(o.id); !ok {
		t.Fatal("the rig does not remember a read nothing wrote during; the test would pass for any code")
	}

	slow := newHeldClient(o.cl)
	set := settings.New(settings.Options{Devices: o.st, Client: slow})
	a := &runtime{settings: set}
	done := make(chan error, 1)
	go func() { _, err := set.Read(context.Background(), o.id); done <- err }()
	select {
	case <-slow.held:
	case <-time.After(5 * time.Second):
		t.Fatal("the read did not get under way")
	}
	write(a, o)
	close(slow.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the read under way: %v (the test needs it to end with values)", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the read did not end")
	}
	_, remembered = set.Last(o.id)
	return remembered
}

// writerClient is the device side of a writer under test: it answers its write with err and what a device
// answers to an OtaUrl, and sends nothing anywhere.
type writerClient struct {
	tasmota.Client
	err error
}

func (w writerClient) Command(_ context.Context, _ tasmota.Target, cmnd string) (json.RawMessage, error) {
	if w.err != nil {
		return nil, w.err
	}
	if cmnd == "OtaUrl http://fw.example/tasmota.bin" || cmnd == "OtaUrl http://fw.example/old.bin" {
		return json.RawMessage(`{"OtaUrl":"` + cmnd[len("OtaUrl "):] + `"}`), nil
	}
	return json.RawMessage(`{"Template":"Invalid JSON or Bad Chip Type"}`), nil
}

func (w writerClient) RestoreConfig(context.Context, tasmota.Target, []byte) error { return w.err }

// Each writer below changes device settings without Settings.Apply. Why (#676, from #662): the service
// remembers the values of its last read as "last known"; a read that started before a write holds the old
// values, so unless the writer tells the service (Forget) it stores them as current, stamped "just read",
// and the tab shows the old OtaUrl, template or settings as if the write had not happened. The write
// failing does not change that: a lost reply leaves open whether the device took it.

func TestAnOtaUrlWrittenByAnUpdateIsNotOverwrittenByAReadFromBeforeIt(t *testing.T) {
	for name, err := range map[string]error{"the device took it": nil, "the reply was lost": tasmota.ErrTimeout} {
		t.Run(name, func(t *testing.T) {
			// Setting the URL for the update and putting the old one back are the same engine call.
			for _, url := range []string{"http://fw.example/tasmota.bin", "http://fw.example/old.bin"} {
				remembered := readRacesWrite(t, func(a *runtime, o anotherDevice) {
					a.svc = &Services{Client: writerClient{err: err}}
					_ = a.otaDeviceAPI().SetOtaURL(t.Context(), o.stored, url)
				})
				if remembered {
					t.Errorf("OtaUrl %s: a read that started before the write is remembered as the current values", url)
				}
			}
		})
	}
}

func TestATemplateSentByATemplateJobIsNotOverwrittenByAReadFromBeforeIt(t *testing.T) {
	for name, err := range map[string]error{"the device refused it": nil, "the reply was lost": tasmota.ErrTimeout} {
		t.Run(name, func(t *testing.T) {
			remembered := readRacesWrite(t, func(a *runtime, o anotherDevice) {
				a.templates = templates.New(templates.Options{Devices: tplDevices{o.stored}, Client: writerClient{err: err}, Backups: noBackups{}})
				_ = a.templateApplier().Send(t.Context(), o.stored, `Template {"NAME":"x","GPIO":[0],"FLAG":0,"BASE":18}`)
			})
			if remembered {
				t.Error("a read that started before the template was sent is remembered as the current values")
			}
		})
	}
}

func TestAConfigRestoredByARestoreIsNotOverwrittenByAReadFromBeforeIt(t *testing.T) {
	for name, err := range map[string]error{"the device took it": nil, "the outcome is unknown": tasmota.ErrTimeout} {
		t.Run("over HTTP, "+name, func(t *testing.T) {
			remembered := readRacesWrite(t, func(a *runtime, o anotherDevice) {
				c := a.restoreClient(writerClient{err: err})
				cr, ok := c.(tasmota.ConfigRestorer)
				if !ok {
					t.Fatal("the client the restore gets cannot restore over HTTP any more")
				}
				_ = cr.RestoreConfig(t.Context(), tasmota.TargetFor(o.stored), []byte("dump"))
			})
			if remembered {
				t.Error("a read that started before the restore is remembered as the current values")
			}
		})
		t.Run("over MQTT, "+name, func(t *testing.T) {
			remembered := readRacesWrite(t, func(a *runtime, o anotherDevice) {
				_ = a.restoreUploader(uploaderReturning{err: err}).UploadConfig(t.Context(), tasmota.TargetFor(o.stored), []byte("dump"), transport.UploadBinding{})
			})
			if remembered {
				t.Error("a read that started before the restore is remembered as the current values")
			}
		})
	}
}

// uploaderReturning is the MQTT route of a restore: it ends with err.
type uploaderReturning struct{ err error }

func (u uploaderReturning) UploadConfig(context.Context, tasmota.Target, []byte, transport.UploadBinding) error {
	return u.err
}

// Why: the restore reads the device's files through the same client and decides by what it implements;
// the wrapper that tells the Settings service about the write must not take the file access (or the
// restore upload) away, or a restore silently stops carrying the files.
func TestTheRestoreClientKeepsWhatTheRestoreAsksOfIt(t *testing.T) {
	o := newAnotherDevice(t) // the real HTTP client implements both
	a := &runtime{}
	c := a.restoreClient(o.cl)
	if _, ok := c.(tasmota.ConfigRestorer); !ok {
		t.Error("the restore upload is gone")
	}
	if _, ok := c.(tasmota.FileSystem); !ok {
		t.Error("the file access is gone")
	}
	if _, ok := a.restoreClient(writerClient{}).(tasmota.FileSystem); ok {
		t.Error("a client without file access gained one")
	}
}

// A replacement sends the old device's backup to a bare address, which has no inventory ID: its own
// RestoreConfig reports ID 0. The Settings values the tab remembers belong to the inventory entry, and
// that is what takes over the new board (SwapHardware). Without a report there, the tab keeps showing the
// old board's values as "last known" for a device that now runs a restored, possibly older, backup.
func TestAnEntryTakenOverByNewHardwareIsNotOverwrittenByAReadFromBeforeIt(t *testing.T) {
	for name, wrongMAC := range map[string]bool{"the entry took the new hardware": false, "the takeover failed": true} {
		t.Run(name, func(t *testing.T) {
			remembered := readRacesWrite(t, func(a *runtime, o anotherDevice) {
				a.svc = &Services{Store: o.st}
				expect := o.stored.MAC
				if wrongMAC {
					expect = "02:00:00:00:00:77" // an admin edited the entry: the swap is refused
				}
				_, err := a.restoreOptions().Hardware.SwapHardware(t.Context(), o.id, store.HardwareSwap{
					ExpectMACs: []string{expect}, MAC: "02:00:00:00:00:98", Address: o.stored.Address, Port: o.stored.Port,
				})
				if (err != nil) != wrongMAC {
					t.Fatalf("SwapHardware error = %v, want an error: %v", err, wrongMAC)
				}
			})
			if remembered {
				t.Error("a read that started before the takeover is remembered as the current values")
			}
		})
	}
}

// The wiring is where these protections silently disappear (see TestRuntimeWiresTheLastKnownValues): the
// wrappers pass on their own, and a setup that handed the raw client, router or store to the restore would
// leave every test above green. These go through what setupRestore and setupOTA really hand over.
func TestTheRestoreIsBuiltFromTheWritersThatReport(t *testing.T) {
	setup := func(a *runtime, o anotherDevice, err error) restore.Options {
		a.svc = &Services{Client: writerClient{err: err}, Router: transport.NewRouter(transport.Options{}), Store: o.st}
		return a.restoreOptions()
	}
	t.Run("restore over HTTP", func(t *testing.T) {
		if readRacesWrite(t, func(a *runtime, o anotherDevice) {
			cr, ok := setup(a, o, tasmota.ErrTimeout).Client.(tasmota.ConfigRestorer)
			if !ok {
				t.Fatal("the restore's client cannot restore over HTTP")
			}
			_ = cr.RestoreConfig(t.Context(), tasmota.TargetFor(o.stored), []byte("dump"))
		}) {
			t.Error("the restore's client does not tell the Settings service what it wrote")
		}
	})
	t.Run("restore over MQTT", func(t *testing.T) {
		if readRacesWrite(t, func(a *runtime, o anotherDevice) {
			// No MQTT is connected: the router ends the upload with an error, as a lost one would.
			_ = setup(a, o, nil).Uploader.UploadConfig(t.Context(), tasmota.TargetFor(o.stored), []byte("dump"), transport.UploadBinding{})
		}) {
			t.Error("the restore's uploader does not tell the Settings service what it wrote")
		}
	})
	t.Run("takeover of the entry", func(t *testing.T) {
		if readRacesWrite(t, func(a *runtime, o anotherDevice) {
			_, _ = setup(a, o, nil).Hardware.SwapHardware(t.Context(), o.id, store.HardwareSwap{ExpectMACs: []string{"02:00:00:00:00:77"}})
		}) {
			t.Error("the restore's inventory access does not tell the Settings service what it took over")
		}
	})
}

// noFirmware is the firmware cache of an engine that never runs a job here.
type noFirmware struct{ firmware.Cache }

func TestTheOtaEngineIsBuiltFromAnApiThatReports(t *testing.T) {
	var handed ota.DeviceAPI
	prev := newOTAEngine
	newOTAEngine = func(o ota.Options, j store.JobStore, d store.DeviceStore, api ota.DeviceAPI, fw ota.Firmware, b ota.Backups, c clock.Clock, n ota.Notifier) *ota.Engine {
		handed = api
		return prev(o, j, d, api, fw, b, c, n)
	}
	t.Cleanup(func() { newOTAEngine = prev })
	remembered := readRacesWrite(t, func(a *runtime, o anotherDevice) {
		bg, stop := context.WithCancel(context.Background())
		a.bgContext, a.log, a.cache = bg, slog.Default(), noFirmware{}
		a.svc = &Services{Client: writerClient{err: tasmota.ErrTimeout}, Store: o.st}
		t.Cleanup(func() { stop(); a.bgWG.Wait() })
		a.setupOTA()
		if handed == nil {
			t.Fatal("setupOTA built no engine")
		}
		_ = handed.SetOtaURL(t.Context(), o.stored, "http://fw.example/tasmota.bin")
	})
	if remembered {
		t.Error("the engine's device access does not tell the Settings service what it wrote")
	}
}

// Why: the engine asks its api for the image upload and the safeboot switch by type assertion and falls back
// to the download when it finds neither; the wrapper that reports the OtaUrl writes must carry both of
// the real api, or a push update is switched off without a word (and a wrapper that claimed them over an
// api without them would panic on a nil method instead).
func TestTheOtaApiKeepsWhatTheEngineAsksOfIt(t *testing.T) {
	api := (&runtime{svc: &Services{Client: newAnotherDevice(t).cl}}).otaDeviceAPI()
	// The wrapper embeds both interfaces, so a type assertion on it always succeeds; what the engine
	// calls afterwards panics on a nil field. Check what the wrapper holds.
	f, ok := api.(forgetOtaAPI)
	if !ok {
		t.Fatalf("otaDeviceAPI is %T, not the reporting wrapper", api)
	}
	if f.FirmwareUploader == nil {
		t.Error("the image upload is gone")
	}
	if f.SafebootAPI == nil {
		t.Error("the safeboot switch is gone")
	}
}
