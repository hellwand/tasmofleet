package sysconfig

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/hellwand/tasmofleet/internal/store"
)

// Why (#720): the operator must be told once, at start, that the client secret in the container locked the
// issuer and which variable is missing. Load carries that paragraph as a warning; the line that reports the
// skipped stored issuer must name the variable too, but in a short form. Repeating the paragraph word for
// word made the start log say the same thing twice and the documentation ("the start log says so in one
// warning") untrue in exactly the scenario it describes: stored issuer, mode, client ID and groups, only the
// secret in the environment.
func TestSecretWithoutIssuerIsExplainedOnceInTheStartLog(t *testing.T) {
	const secret = "fake-oidc-secret-720"
	st := newMem()
	st.rows["OIDC_ISSUER"] = store.SystemSetting{Key: "OIDC_ISSUER", Value: "https://idp.example/"}
	st.rows["OIDC_CLIENT_ID"] = store.SystemSetting{Key: "OIDC_CLIENT_ID", Value: "tasmofleet"}
	st.rows["AUTH_MODE"] = store.SystemSetting{Key: "AUTH_MODE", Value: "oidc"}
	cfg, err := loadCfg(baseEnv("TASMOFLEET_PUBLIC_URL=https://tf.example", "TASMOFLEET_OIDC_CLIENT_SECRET="+secret))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "TASMOFLEET_OIDC_ISSUER") {
		t.Fatalf("Load must carry one warning that names the missing variable: %q", cfg.Warnings)
	}
	note := cfg.Warnings[0]
	var logs bytes.Buffer
	if _, err := New(context.Background(), Options{Base: cfg, Store: st, Audit: &memAudit{}, Log: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}); err != nil {
		t.Fatal(err)
	}
	startLog := strings.Join(cfg.Warnings, "\n") + "\n" + logs.String()
	if n := strings.Count(startLog, note); n != 1 {
		t.Errorf("the full note must appear once in the start log, it appears %d times:\n%s", n, startLog)
	}
	var skipLine string
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "stored setting ignored") && strings.Contains(line, "key=OIDC_ISSUER") {
			skipLine = line
		}
	}
	if !strings.Contains(skipLine, "TASMOFLEET_OIDC_ISSUER") {
		t.Errorf("the line for the skipped issuer must name the missing variable: %q", skipLine)
	}
	if strings.Contains(startLog, secret) {
		t.Error("the start log carries the environment secret")
	}
}
