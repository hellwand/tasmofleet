package app

import (
	"context"

	"github.com/hellwand/tasmofleet/internal/model"
	"github.com/hellwand/tasmofleet/internal/ota"
	"github.com/hellwand/tasmofleet/internal/restore"
	"github.com/hellwand/tasmofleet/internal/store"
	"github.com/hellwand/tasmofleet/internal/tasmota"
	"github.com/hellwand/tasmofleet/internal/transport"
)

// The writers of device settings other than the Settings service's own Apply tell the service what they
// did (settingsWritten, #662): a read that started before the write holds the old values and must not be
// remembered as current (#676). A write counts as done whatever it returns: an error that is not a
// certain "never sent" (a lost reply, a timeout) leaves open whether the device took it, and a read that
// is forgotten costs one device read while a stale one shows the old values as just read.
//
// The wrappers live here, in the app, because the packages of these writers (ota, restore) are SAFETY
// packages that know nothing of the Settings service.

// otaDeviceAPI is the engine's device access: ota.NewDeviceAPI over the router, with every OtaUrl write
// reported. Setting the URL for an update and putting the old one back are both ActSetOtaURL /
// ActRestoreOtaURL calls of SetOtaURL; nothing else the engine sends changes a Settings value.
func (a *runtime) otaDeviceAPI() ota.DeviceAPI {
	api := ota.NewDeviceAPI(a.svc.Client)
	up, _ := api.(ota.FirmwareUploader)
	sb, _ := api.(ota.SafebootAPI)
	return forgetOtaAPI{DeviceAPI: api, FirmwareUploader: up, SafebootAPI: sb, written: a.settingsWritten}
}

// forgetOtaAPI embeds the optional interfaces too: the engine asks its api for them with a type
// assertion (the image upload, the safeboot switch), and a wrapper that hid them would turn those
// updates off.
type forgetOtaAPI struct {
	ota.DeviceAPI
	ota.FirmwareUploader
	ota.SafebootAPI
	written func(model.DeviceID)
}

func (f forgetOtaAPI) SetOtaURL(ctx context.Context, d model.Device, url string) error {
	err := f.DeviceAPI.SetOtaURL(ctx, d, url)
	f.written(d.ID)
	return err
}

// forgetTemplateApplier reports the Template command of a GPIO template job.
type forgetTemplateApplier struct {
	ota.TemplateApplier
	written func(model.DeviceID)
}

func (f forgetTemplateApplier) Send(ctx context.Context, d model.Device, command string) error {
	err := f.TemplateApplier.Send(ctx, d, command)
	f.written(d.ID)
	return err
}

// restoreClient is the restore's device client: c, with the restore upload (HTTP) reported. The restore
// asks its client for tasmota.ConfigRestorer and tasmota.FileSystem by type assertion and does without
// what it does not find, so the result implements exactly what c implements of the two.
func (a *runtime) restoreClient(c tasmota.Client) tasmota.Client {
	cr, ok := c.(tasmota.ConfigRestorer)
	if !ok {
		return c // no restore over HTTP: nothing to report
	}
	w := forgetRestorer{Client: c, restorer: cr, written: a.settingsWritten}
	if fs, ok := c.(tasmota.FileSystem); ok {
		return forgetRestorerFS{forgetRestorer: w, FileSystem: fs}
	}
	return w
}

type forgetRestorer struct {
	tasmota.Client
	restorer tasmota.ConfigRestorer
	written  func(model.DeviceID)
}

func (f forgetRestorer) RestoreConfig(ctx context.Context, t tasmota.Target, dump []byte) error {
	err := f.restorer.RestoreConfig(ctx, t, dump)
	f.written(t.DeviceID())
	return err
}

// forgetRestorerFS adds the file access of the client behind it, unchanged: the files a restore writes are
// not Settings values.
type forgetRestorerFS struct {
	forgetRestorer
	tasmota.FileSystem
}

// restoreUploader is the restore's MQTT route (FileUpload): u, with the upload reported.
func (a *runtime) restoreUploader(u transport.ConfigUploader) transport.ConfigUploader {
	return forgetUploader{ConfigUploader: u, written: a.settingsWritten}
}

type forgetUploader struct {
	transport.ConfigUploader
	written func(model.DeviceID)
}

func (f forgetUploader) UploadConfig(ctx context.Context, t tasmota.Target, dump []byte, bind transport.UploadBinding) error {
	err := f.ConfigUploader.UploadConfig(ctx, t, dump, bind)
	f.written(t.DeviceID())
	return err
}

// restoreHardware is the restore's inventory access: h, with the takeover of an entry by new hardware
// reported. A replacement sends the old device's backup to a bare address (no inventory ID, so its own
// RestoreConfig and UploadConfig report ID 0); what holds the Settings values the tab remembers is the
// entry that takes over the new board, and SwapHardware is where the restore names it.
func (a *runtime) restoreHardware(h restore.Hardware) restore.Hardware {
	return forgetHardware{Hardware: h, written: a.settingsWritten}
}

type forgetHardware struct {
	restore.Hardware
	written func(model.DeviceID)
}

func (f forgetHardware) SwapHardware(ctx context.Context, id model.DeviceID, sw store.HardwareSwap) ([]string, error) {
	macs, err := f.Hardware.SwapHardware(ctx, id, sw)
	f.written(id)
	return macs, err
}
