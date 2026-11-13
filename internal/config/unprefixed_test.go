package config

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Why (#721): the container environment only reads TASMOFLEET_ keys, so a sign-in variable written as
// the provider's guide names it (OIDC_CLIENT_SECRET_FILE=...) does nothing, even with a path that does
// not exist, and the person sees a login page without single sign-on and no hint. One start warning
// names the key that would work. The value stays ignored (no alias) and is never printed.
func TestUnprefixedSignInKeyWarnsOnceAndStaysIgnored(t *testing.T) {
	const secretPath = "/run/secrets/does-not-exist-9f3a"
	c, err := Load(base(t.TempDir(), "OIDC_CLIENT_SECRET_FILE="+secretPath))
	if err != nil {
		t.Fatalf("an unprefixed key must not stop the start (and its file is not opened): %v", err)
	}
	if len(c.Warnings) != 1 {
		t.Fatalf("warnings = %q, want exactly one", c.Warnings)
	}
	w := c.Warnings[0]
	if !strings.Contains(w, "TASMOFLEET_OIDC_CLIENT_SECRET_FILE") {
		t.Errorf("the warning does not name the key that works: %q", w)
	}
	if strings.Contains(w, secretPath) {
		t.Errorf("the value reached the warning: %q", w)
	}
	if c.OIDCClientSecret != "" || c.Locked["OIDC_CLIENT_SECRET"] {
		t.Errorf("the unprefixed key was used as an alias: secret %q, locked %v", c.OIDCClientSecret, c.Locked)
	}
}

// Why (#721): an unprefixed AUTH_MODE must not win over, or replace, the real setting.
func TestUnprefixedValueNeverReplacesThePrefixedOne(t *testing.T) {
	c, err := Load(base(t.TempDir(), "AUTH_MODE=oidc", "PUBLIC_URL=https://x.example"))
	if err != nil {
		t.Fatal(err)
	}
	if c.AuthMode != "header" || c.PublicURL != "" {
		t.Errorf("an unprefixed value was applied: mode %q, public url %q", c.AuthMode, c.PublicURL)
	}
	if len(c.Warnings) != 2 {
		t.Errorf("warnings = %q, want one per key", c.Warnings)
	}
}

// Why (#721): every key of the sign-in set is covered, not only the one from the report. The set is
// the issue's list: AUTH_MODE, PUBLIC_URL, OIDC_*, ADMIN_GROUPS, OPERATOR_GROUPS and the _FILE variant
// of the secret. A value is never printed.
func TestEveryKnownSignInKeyWarnsWithoutPrefix(t *testing.T) {
	keys := []string{"AUTH_MODE", "PUBLIC_URL", "ADMIN_GROUPS", "OPERATOR_GROUPS",
		"OIDC_ISSUER", "OIDC_CLIENT_ID", "OIDC_CLIENT_SECRET", "OIDC_CLIENT_SECRET_FILE", "OIDC_SCOPES",
		"OIDC_GROUPS_CLAIM", "OIDC_USERNAME_CLAIM", "OIDC_NAME_CLAIM", "OIDC_DISPLAY_NAME", "OIDC_POST_LOGOUT_URL", "OIDC_RECHECK"}
	// Every OIDC_ key the Config reads must be in the list above: a new one is not forgotten.
	rt := reflect.TypeOf(Config{})
	for i := range rt.NumField() {
		if k := rt.Field(i).Tag.Get("env"); strings.HasPrefix(k, "OIDC_") && !slices.Contains(keys, k) {
			t.Errorf("Config reads %s, the test (and the warning) does not know it", k)
		}
	}
	for _, k := range keys {
		t.Run(k, func(t *testing.T) {
			c, err := Load(base(t.TempDir(), k+"=value-that-must-not-be-printed"))
			if err != nil {
				t.Fatal(err)
			}
			if len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], Prefix+k) {
				t.Fatalf("warnings = %q, want one naming %s%s", c.Warnings, Prefix, k)
			}
			if strings.Contains(c.Warnings[0], "value-that-must-not-be-printed") {
				t.Errorf("the value reached the warning: %q", c.Warnings[0])
			}
		})
	}
}

// Why (#721): a warning that fires for names the program never read would teach people to ignore it.
// Other unprefixed names (LISTEN, DATA_DIR, the shell's own) and an empty value ("KEY=" is what
// Unraid passes for an unfilled field, and counts as not set) stay silent.
func TestUnprefixedNamesThatAreNotSignInKeysStaySilent(t *testing.T) {
	c, err := Load(base(t.TempDir(), "LISTEN=:9999", "DATA_DIR=/elsewhere", "HOME=/root", "OIDC_ISSUER=", "AUTH_MODE=  "))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Warnings) != 0 {
		t.Errorf("warnings = %q, want none", c.Warnings)
	}
}

// Why (#721): in tasmofleet.env the prefix is optional on purpose, so short names there are correct
// and are applied; warning about them would be a false alarm on a valid file.
func TestShortNamesInTheEnvFileDoNotWarn(t *testing.T) {
	dir := t.TempDir()
	file := "OIDC_ISSUER=https://auth.example.com/application/o/tasmofleet/\nOIDC_CLIENT_ID=abc\nPUBLIC_URL=https://x.example\nADMIN_GROUPS=admins\n"
	if err := os.WriteFile(filepath.Join(dir, "tasmofleet.env"), []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(base(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Warnings) != 0 {
		t.Errorf("warnings = %q, want none for short names in the file", c.Warnings)
	}
	if c.PublicURL != "https://x.example" {
		t.Errorf("the file value was not applied: %q", c.PublicURL)
	}
}
