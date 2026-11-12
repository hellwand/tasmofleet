package web

import (
	"fmt"
	"strings"
	"time"
)

// Tooltips. The UI uses the native title attribute: it needs no script, works
// with the strict CSP, and is read out as the accessible description by the
// browsers. hints is the one place that explains the status words and chips
// that carry a title; templates call h.hint(word) and the text is never
// repeated by hand. It holds exactly the words a call site can pass: the
// health state (cells.templ), the job state (updates.go) and "no-ota"
// (cells.templ); TestHintsAreExactlyTheWordsACallSiteCanPass keeps it so.
// "stale" is built by hintText from the configured threshold. Plain data
// cells get no title (the cut-off ones get one from tables.js).
var hints = map[string]string{
	// device health
	"online":      "Online: reports to the broker or answered the last HTTP poll.",
	"offline":     "Offline: reported offline, or no answer to HTTP polls.",
	"auth_failed": "Auth failed: the device refused the stored web login.",
	"unknown":     "Unknown: nothing has been heard from this device yet.",
	// OTA job states
	"queued":           "Queued: waiting for a free update slot.",
	"preflight":        "Checking: reading the device and the firmware file.",
	"backup":           "Backing up: saving the configuration first.",
	"set_ota_url":      "Preparing: pointing the device at the firmware server.",
	"upgrade_issued":   "Flashing: the device is downloading and installing the firmware.",
	"await_reboot":     "Rebooting: waiting for the device to come back.",
	"safeboot_switch":  "Restarting into safeboot: the image does not fit the running firmware, so the device restarts into its safeboot partition and takes the image there.",
	"safeboot_recover": "Switching back: the upload into safeboot did not work out; the device is sent back to its firmware.",
	"applying":         "Applying: sending the policy commands and saving them to the device once.",
	"verify":           "Verifying: checking that the device runs the new firmware.",
	"restore_ota_url":  "Cleaning up: putting the device's original update address back.",
	"succeeded":        "Succeeded: the device runs the target firmware.",
	"failed":           "Failed: the update did not finish; the device keeps its old state.",
	"cancelled":        "Cancelled: stopped before it finished.",
	// the device list's tag
	"no-ota": "Excluded from OTA updates: never offered firmware.",
}

// hintText explains a status word or chip; staleAfter is the configured
// silence after which an MQTT device counts as stale.
func hintText(v any, staleAfter func() time.Duration) string {
	s := asString(v)
	if s == "stale" {
		d := 11 * time.Minute // health.DefaultStaleAfter
		if staleAfter != nil {
			d = staleAfter()
		}
		return fmt.Sprintf("Stale: the device is online at the broker, but sent no message for %s.", humanDuration(d))
	}
	return hints[s]
}

// familyHint explains a chip family chip: the safety rule behind it.
func familyHint(family string) string {
	if family == "" {
		return "Unknown chip: not updated until the chip is detected."
	}
	name := strings.ToUpper(family)
	if len(name) > 5 && strings.HasPrefix(name, "ESP32") {
		name = "ESP32-" + name[5:] // esp32s3 -> ESP32-S3
	}
	return fmt.Sprintf("%s devices get only firmware built for this chip.", name)
}
