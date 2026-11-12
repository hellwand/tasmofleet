package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The OIDC issuer and the client secret lock together on purpose (a secret the operator fixed must
// only go to the issuer the operator fixed). The lock itself is not what these tests are about; they
// are about what the operator is told when only one of the two is in the environment (#720).

const fakeClientSecret = "fake-oidc-secret-720"

func oidcEnv(dir string, extra ...string) []string {
	return append([]string{"TASMOFLEET_DATA_DIR=" + dir, "TASMOFLEET_CONFIG_FILE=/dev/null"}, extra...)
}

// Why (#720): with only the client secret in the environment the issuer is locked to an empty value, so a
// stored issuer is skipped and stored single sign-on is off. Nothing said so: the operator saw local sign-in
// only. One warning at start must name the missing variable, the reason (the secret locked it) and the
// outcome, and must neither fail the start (local sign-in is the way back in) nor carry the secret.
func TestClientSecretWithoutIssuerWarnsOnceAndStillStarts(t *testing.T) {
	dir := t.TempDir()
	secretFile := filepath.Join(dir, "oidc-secret")
	if err := os.WriteFile(secretFile, []byte(fakeClientSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, env := range map[string][]string{
		"variable": oidcEnv(dir, "TASMOFLEET_OIDC_CLIENT_SECRET="+fakeClientSecret),
		"file":     oidcEnv(dir, "TASMOFLEET_OIDC_CLIENT_SECRET_FILE="+secretFile),
	} {
		t.Run(name, func(t *testing.T) {
			c, err := Load(env)
			if err != nil {
				t.Fatalf("the start must not fail: %v", err)
			}
			if c.AuthMode != "local" {
				t.Fatalf("local sign-in must stay available, mode %q", c.AuthMode)
			}
			if len(c.Warnings) != 1 {
				t.Fatalf("want exactly one warning, got %q", c.Warnings)
			}
			w := c.Warnings[0]
			for _, want := range []string{"TASMOFLEET_OIDC_ISSUER", "TASMOFLEET_OIDC_CLIENT_SECRET", "locked", "single sign-on is off"} {
				if !strings.Contains(w, want) {
					t.Errorf("the warning lacks %q: %s", want, w)
				}
			}
			if strings.Contains(w, fakeClientSecret) || strings.Contains(w, secretFile) {
				t.Errorf("the warning carries the secret or its path: %s", w)
			}
		})
	}
}

// Why (#720): the warning is for the half-configured pair only. Both variables, none, an empty secret
// variable (Unraid passes unfilled fields as "KEY=") and an issuer alone (a public client needs no secret)
// are legitimate and must start silently.
func TestNoOIDCPairWarningWhenThePairIsComplete(t *testing.T) {
	for name, env := range map[string][]string{
		"both":                oidcEnv(t.TempDir(), "TASMOFLEET_OIDC_ISSUER=https://idp.example/", "TASMOFLEET_OIDC_CLIENT_SECRET="+fakeClientSecret),
		"neither":             oidcEnv(t.TempDir()),
		"blank secret":        oidcEnv(t.TempDir(), "TASMOFLEET_OIDC_CLIENT_SECRET="),
		"issuer only":         oidcEnv(t.TempDir(), "TASMOFLEET_OIDC_ISSUER=https://idp.example/"),
		"blank secret + only": oidcEnv(t.TempDir(), "TASMOFLEET_OIDC_CLIENT_SECRET=   ", "TASMOFLEET_OIDC_ISSUER=https://idp.example/"),
	} {
		t.Run(name, func(t *testing.T) {
			c, err := Load(env)
			if err != nil {
				t.Fatal(err)
			}
			if len(c.Warnings) != 0 {
				t.Fatalf("unexpected warnings: %q", c.Warnings)
			}
		})
	}
}

// Why (#720): a stored value that is skipped must say why. The issuer that the environment's client
// secret locked used to read "set by the environment", which nobody did; the operator looked for a
// variable that does not exist. It now says the secret locked it and which variable to add. The same
// holds the other way round: an issuer from the environment locks the secret field. A key the
// environment really set keeps the plain wording.
func TestOverlayNamesThePartnerThatLockedTheKey(t *testing.T) {
	stored := map[string]string{"OIDC_ISSUER": "https://idp.example/", "OIDC_CLIENT_ID": "stored-id", "OIDC_CLIENT_SECRET": "stored-secret"}

	secretOnly, err := Load(oidcEnv(t.TempDir(), "TASMOFLEET_OIDC_CLIENT_SECRET="+fakeClientSecret, "TASMOFLEET_OIDC_CLIENT_ID=env-id"))
	if err != nil {
		t.Fatal(err)
	}
	got, skipped := Overlay(secretOnly, stored)
	if got.OIDCIssuer != "" {
		t.Fatalf("the locked issuer must stay empty, got %q", got.OIDCIssuer)
	}
	why := skipped["OIDC_ISSUER"].Error()
	if why == "set by the environment" || !strings.Contains(why, "TASMOFLEET_OIDC_ISSUER") || !strings.Contains(why, "client secret") {
		t.Errorf("issuer reason: %q", why)
	}
	if skipped["OIDC_CLIENT_ID"].Error() != "set by the environment" {
		t.Errorf("a key the environment set keeps the plain reason: %q", skipped["OIDC_CLIENT_ID"])
	}
	if strings.Contains(why, fakeClientSecret) || strings.Contains(skipped["OIDC_CLIENT_SECRET"].Error(), fakeClientSecret) {
		t.Error("a skip reason carries the environment secret")
	}

	issuerOnly, err := Load(oidcEnv(t.TempDir(), "TASMOFLEET_OIDC_ISSUER=https://env-idp.example/"))
	if err != nil {
		t.Fatal(err)
	}
	_, skipped = Overlay(issuerOnly, stored)
	why = skipped["OIDC_CLIENT_SECRET"].Error()
	if why == "set by the environment" || !strings.Contains(why, "TASMOFLEET_OIDC_CLIENT_SECRET") || !strings.Contains(why, "issuer") {
		t.Errorf("client secret reason: %q", why)
	}
	if skipped["OIDC_ISSUER"].Error() != "set by the environment" {
		t.Errorf("the issuer the environment set keeps the plain reason: %q", skipped["OIDC_ISSUER"])
	}
}
