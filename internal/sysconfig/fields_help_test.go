package sysconfig

import (
	"strings"
	"testing"
)

// Why: every editable system setting explains itself on the page (the hint
// under the field); a new setting without one would be a bare input.
func TestEveryFieldHasHelp(t *testing.T) {
	for _, f := range fields {
		if f.Help == "" {
			t.Errorf("setting %s has no help text", f.Key)
		}
	}
}

// Why (#604): "problems" also sends a scheduled update that was not started (TasmoFleet was down past its
// start time; nothing on the devices changed, and nobody would notice otherwise). The help under the setting is
// where a person decides between "any" and "problems", so it must not promise "only failed or partial ones".
func TestNotifyUpdateWhenHelpNamesTheScheduledUpdateThatWasNotStarted(t *testing.T) {
	for _, f := range fields {
		if f.Key != "NOTIFY_UPDATE_WHEN" {
			continue
		}
		if !strings.Contains(f.Help, "not started") {
			t.Errorf("help of %s = %q, want it to say that a scheduled update that was not started is sent too", f.Key, f.Help)
		}
		return
	}
	t.Fatal("setting NOTIFY_UPDATE_WHEN not found")
}
