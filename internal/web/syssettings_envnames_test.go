package web

import (
	"context"
	"html"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hellwand/tasmofleet/internal/config"
	"github.com/hellwand/tasmofleet/internal/store"
	"github.com/hellwand/tasmofleet/internal/sysconfig"
)

var htmlTag = regexp.MustCompile(`<[^>]*>`)

// pageText is what a person reads on a page: tags gone, entities decoded.
func pageText(page string) string { return html.UnescapeString(htmlTag.ReplaceAllString(page, " ")) }

// sysEnvStored is sysEnvFull with settings already stored in the database, as after an earlier
// start without the environment variables.
func sysEnvStored(t *testing.T, rows map[string]store.SystemSetting, extraEnv ...string) *env {
	t.Helper()
	cfg, err := config.Load(append([]string{
		"TASMOFLEET_DATA_DIR=/nonexistent-tasmofleet-test", "TASMOFLEET_CONFIG_FILE=/dev/null",
	}, extraEnv...))
	if err != nil {
		t.Fatal(err)
	}
	aud := &fakeAudit{}
	svc, err := sysconfig.New(context.Background(), sysconfig.Options{Base: cfg, Store: &sysMem{rows: rows}, Audit: aud,
		TestMQTT: func(context.Context, sysconfig.MQTTParams) error { return nil },
		TestSSO:  func(context.Context, string) (string, error) { return "discovery OK", nil }})
	if err != nil {
		t.Fatal(err)
	}
	st := &atomic.Bool{}
	st.Store(true)
	return setupWith(t, func(d *Deps) {
		d.SysConfig = svc
		d.Guard = stepGuard{fakeGuard: fakeGuard{}, stepped: st}
		d.Recorder = aud
	})
}

// Why (#719): in the container only TASMOFLEET_-prefixed names are read; a short name there is ignored
// without a message. A hint that tells the person which variable to set must therefore print the
// prefixed name, or they set a variable that does nothing. The page is rendered with every sign-in
// setting fixed by the environment and nothing stored, and no key of the configuration may appear in
// its text without the prefix: not in a locked field's help, not in "Needs ...", not in the
// "Set by the container" card, not in a field's info text.
func TestSettingsPageNamesVariablesOnlyWithThePrefix(t *testing.T) {
	var env []string
	for _, k := range config.Keys() {
		if k.Lock && !k.Secret && k.Key != "AUTH_MODE" {
			env = append(env, "TASMOFLEET_"+k.Key+"=x-"+strings.ToLower(k.Key))
		}
	}
	env = append(env, "TASMOFLEET_OIDC_CLIENT_SECRET=fake-env-secret-719", "TASMOFLEET_METRICS_TOKEN=fake-env-token-719",
		"TASMOFLEET_OIDC_ISSUER=https://idp.example/", "TASMOFLEET_LOG_LEVEL=info")
	e := sysEnvStored(t, map[string]store.SystemSetting{}, env...)
	_, page := e.do(t, "GET", "/settings/system", "admin", nil)
	text := pageText(page)
	if !strings.Contains(text, "TASMOFLEET_METRICS_TOKEN in the container configuration") {
		t.Errorf("a locked field must name the prefixed variable:\n%s", fieldHTML(t, page, "METRICS_TOKEN"))
	}
	for _, k := range config.Keys() {
		bare := regexp.MustCompile(`(^|[^A-Za-z0-9_])` + k.Key + `([^A-Za-z0-9_]|$)`)
		if loc := bare.FindStringIndex(text); loc != nil {
			from, to := max(loc[0]-60, 0), min(loc[1]+60, len(text))
			t.Errorf("%s appears without the TASMOFLEET_ prefix: ...%s...", k.Key, strings.Join(strings.Fields(text[from:to]), " "))
		}
	}
	if strings.Contains(page, "fake-env-secret-719") || strings.Contains(page, "fake-env-token-719") {
		t.Error("an environment secret reached the page")
	}
}

// Why (#719): the save error and the confirmation banner also send the person to the container
// configuration for a variable (PUBLIC_URL for single sign-on); they must print the prefixed name too.
func TestSettingsMessagesToTheContainerNameThePrefixedVariable(t *testing.T) {
	e, _, _, _ := sysEnv(t, true)
	_, body := e.do(t, "POST", "/settings/system?tab=signin", "admin", url.Values{
		"csrf": {"tok"}, "v.AUTH_MODE": {"oidc"}, "v.OIDC_ISSUER": {"https://idp.example/"}, "v.OIDC_CLIENT_ID": {"id"}, "v.ADMIN_GROUPS": {"adm"},
	})
	text := pageText(body)
	if !strings.Contains(text, "TASMOFLEET_PUBLIC_URL") || regexp.MustCompile(`(^|[^A-Z_])PUBLIC_URL`).MatchString(text) {
		t.Errorf("the save error must name TASMOFLEET_PUBLIC_URL:\n%s", text)
	}
	// A header principal cannot confirm sign-in changes; the banner sends them to the container too.
	_, body = e.do(t, "POST", "/settings/system", "admin", url.Values{"csrf": {"tok"}, "v.OIDC_ISSUER": {"https://idp.example/"}}, "X-Test-Method", "header")
	text = pageText(body)
	if !strings.Contains(text, "(TASMOFLEET_OIDC_ISSUER") || regexp.MustCompile(`[( ]OIDC_ISSUER`).MatchString(text) {
		t.Errorf("the confirmation banner must list the prefixed variables:\n%s", text)
	}
}

// Why (#720): with only the client secret in the environment the issuer is locked to an empty value,
// the stored issuer is skipped and stored single sign-on is off. The banner of ignored values used to
// say "OIDC_ISSUER: set by the environment", which nobody did; the person searched for a variable that
// does not exist. The banner and the locked field must say that the client secret locked the issuer and
// which variable to add. The secret itself never reaches the page.
func TestSignInTabSaysWhyTheIssuerIsLockedWhenOnlyTheSecretIsInTheEnvironment(t *testing.T) {
	e := sysEnvStored(t, map[string]store.SystemSetting{
		"AUTH_MODE":      {Key: "AUTH_MODE", Value: "oidc"},
		"OIDC_ISSUER":    {Key: "OIDC_ISSUER", Value: "https://idp.example/"},
		"OIDC_CLIENT_ID": {Key: "OIDC_CLIENT_ID", Value: "tasmofleet"},
		"ADMIN_GROUPS":   {Key: "ADMIN_GROUPS", Value: "adm"},
	}, "TASMOFLEET_PUBLIC_URL=https://tf.example", "TASMOFLEET_OIDC_CLIENT_SECRET=fake-env-secret-720")
	r, page := e.do(t, "GET", "/settings/system?tab=signin", "admin", nil)
	if r.StatusCode != 200 {
		t.Fatalf("the page must still open: %d", r.StatusCode)
	}
	text := pageText(page)
	if strings.Contains(text, "OIDC_ISSUER : set by the environment") || strings.Contains(text, "OIDC_ISSUER: set by the environment") {
		t.Errorf("the banner blames the environment for an issuer nobody set:\n%s", text)
	}
	ignored := text[strings.Index(text, "Stored values that are ignored"):]
	for _, want := range []string{"TASMOFLEET_OIDC_ISSUER", "client secret", "locked"} {
		if !strings.Contains(ignored, want) {
			t.Errorf("the banner lacks %q:\n%.600s", want, ignored)
		}
	}
	help := pageText(fieldHTML(t, page, "OIDC_ISSUER"))
	if !strings.Contains(help, "TASMOFLEET_OIDC_ISSUER") || !strings.Contains(help, "client secret") {
		t.Errorf("the locked issuer field must say why it is locked and what to set: %s", help)
	}
	if strings.Contains(page, "fake-env-secret-720") {
		t.Error("the environment secret reached the page")
	}
}
