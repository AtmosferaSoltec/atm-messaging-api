package config

import (
	"testing"
	"time"
)

// setRequired sets the variables Load cannot start without.
func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgresql://example")
	t.Setenv("AUTH_PUBLIC_KEY_BASE64", "key")
	t.Setenv("AUTH_ISSUER", "atm-auth-api")
}

func TestLoadAppliesDefaults(t *testing.T) {
	setRequired(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPPort != "8080" || cfg.Limits.PromotionalDailyCap != 200 || cfg.Queue.MaxAttempts != 3 {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
	if cfg.Limits.Location.String() != "America/Lima" {
		t.Errorf("the default time zone is America/Lima, got %s", cfg.Limits.Location)
	}
}

func TestLoadReadsOverrides(t *testing.T) {
	setRequired(t)
	t.Setenv("LIMITS_PROMOTIONAL_MIN_DELAY", "2s")
	t.Setenv("LIMITS_PROMOTIONAL_MAX_DELAY", "4s")
	t.Setenv("LIMITS_PROMOTIONAL_DAILY_CAP", "50")
	t.Setenv("QUEUE_MAX_AGE", "30m")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Limits.PromotionalMinDelay != 2*time.Second || cfg.Limits.PromotionalDailyCap != 50 || cfg.Queue.MaxAge != 30*time.Minute {
		t.Errorf("overrides not applied: %+v", cfg)
	}
}

func TestLoadRejectsInvalidSettings(t *testing.T) {
	cases := map[string]map[string]string{
		"missing database url": {"DATABASE_URL": ""},
		"missing auth key":     {"AUTH_PUBLIC_KEY_BASE64": ""},
		"missing auth issuer":  {"AUTH_ISSUER": ""},
		"bad duration":         {"LIMITS_PROMOTIONAL_MIN_DELAY": "soon"},
		"bad integer":          {"LIMITS_WARMUP_DAYS": "many"},
		"max below min":        {"LIMITS_PROMOTIONAL_MIN_DELAY": "30s", "LIMITS_PROMOTIONAL_MAX_DELAY": "10s"},
		"unknown time zone":    {"LIMITS_TIMEZONE": "Mars/Base"},
		"zero attempts":        {"QUEUE_MAX_ATTEMPTS": "0"},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			setRequired(t)
			for key, value := range env {
				t.Setenv(key, value)
			}
			if _, err := Load(); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestWebhookPrivateTargetsAreOnlyAllowedInDevelopmentByDefault(t *testing.T) {
	setRequired(t)

	t.Setenv("APP_ENV", "development")
	cfg, _ := Load()
	if !cfg.Webhook.AllowPrivate {
		t.Error("development allows private webhook targets by default")
	}

	t.Setenv("APP_ENV", "production")
	cfg, _ = Load()
	if cfg.Webhook.AllowPrivate {
		t.Error("production must block private webhook targets by default")
	}

	t.Setenv("WEBHOOK_ALLOW_PRIVATE", "maybe")
	if _, err := Load(); err == nil {
		t.Error("an invalid boolean must be rejected")
	}
}

func TestWebSettings(t *testing.T) {
	setRequired(t)
	t.Setenv("WEB_CORS_ORIGINS", " https://panel.example.com , http://localhost:5173 ,")
	t.Setenv("PUBLIC_WEB_URL", "https://panel.example.com")
	t.Setenv("CONNECT_LINK_TTL", "6h")
	t.Setenv("HTTP_TRUSTED_PROXIES", "10.0.0.1")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Web.CORSOrigins) != 2 || cfg.Web.CORSOrigins[1] != "http://localhost:5173" ||
		cfg.Web.URL != "https://panel.example.com" || cfg.Web.ConnectLinkTTL != 6*time.Hour {
		t.Errorf("unexpected web settings: %+v", cfg.Web)
	}
	if len(cfg.TrustedProxies) != 1 {
		t.Errorf("unexpected trusted proxies: %v", cfg.TrustedProxies)
	}
}

func TestWebDefaultsAreSafe(t *testing.T) {
	setRequired(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Web.CORSOrigins) != 0 || len(cfg.TrustedProxies) != 0 {
		t.Error("by default no browser origin and no proxy header is trusted")
	}
	if cfg.Web.ConnectLinkTTL != 24*time.Hour {
		t.Errorf("connect links last 24h by default, got %v", cfg.Web.ConnectLinkTTL)
	}
}
