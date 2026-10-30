// Package config loads the application settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/AtmosferaSoltec/atm-messaging-api/internal/limits"
)

// Queue tunes delivery retries and expiry.
type Queue struct {
	MaxAttempts       int
	RetryBase         time.Duration
	NotConnectedRetry time.Duration
	MaxAge            time.Duration
}

// Webhook tunes outgoing webhooks.
type Webhook struct {
	MaxAttempts int
	Timeout     time.Duration
	RetryBase   time.Duration
	// AllowPrivate lets webhooks reach private/loopback addresses and plain http.
	// Only for development; in production it must be off to prevent SSRF.
	AllowPrivate bool
}

// Auth is the public key and issuer of ATM-Auth-API.
type Auth struct {
	PublicKeyBase64 string
	Issuer          string
}

// Enabled reports whether both values are set.
func (a Auth) Enabled() bool { return a.PublicKeyBase64 != "" && a.Issuer != "" }

// Admin configures the single operator of the panel. The admin API is only
// served when both User and Password are set.
type Admin struct {
	User       string
	Password   string
	SessionTTL time.Duration
}

// Enabled reports whether the admin API has credentials to run with.
func (a Admin) Enabled() bool { return a.User != "" && a.Password != "" }

// Web describes the web app that talks to the API.
type Web struct {
	// URL is the web app's public base URL; connect links are built on it.
	URL string
	// CORSOrigins are the origins allowed to call /admin/v1 and /public from a browser.
	CORSOrigins []string
	// ConnectLinkTTL is how long a link for a customer to scan a QR stays valid.
	ConnectLinkTTL time.Duration
}

// Config holds the application settings.
type Config struct {
	Env         string
	HTTPPort    string
	DatabaseURL string
	Limits      limits.Policy
	Queue       Queue
	Webhook     Webhook
	Admin       Admin
	Web         Web
	// Auth is how the service validates the service tokens of ATM-Auth-API.
	Auth Auth
	// TrustedProxies are reverse proxies whose X-Forwarded-For is believed.
	TrustedProxies []string
}

// Load reads the configuration from the environment, applying defaults.
func Load() (Config, error) {
	cfg := Config{
		Env:         getEnv("APP_ENV", "development"),
		HTTPPort:    getEnv("HTTP_PORT", "8080"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}

	policy, err := loadLimits()
	if err != nil {
		return Config{}, err
	}
	cfg.Limits = policy

	queue, err := loadQueue()
	if err != nil {
		return Config{}, err
	}
	cfg.Queue = queue

	webhook, err := loadWebhook(cfg.Env)
	if err != nil {
		return Config{}, err
	}
	cfg.Webhook = webhook

	cfg.Admin = Admin{User: os.Getenv("ADMIN_USER"), Password: os.Getenv("ADMIN_PASSWORD"), SessionTTL: 12 * time.Hour}
	if (cfg.Admin.User == "") != (cfg.Admin.Password == "") {
		return Config{}, errors.New("ADMIN_USER and ADMIN_PASSWORD must be set together")
	}
	cfg.Auth = Auth{PublicKeyBase64: os.Getenv("AUTH_PUBLIC_KEY_BASE64"), Issuer: os.Getenv("AUTH_ISSUER")}
	if !cfg.Auth.Enabled() {
		return Config{}, errors.New("AUTH_PUBLIC_KEY_BASE64 and AUTH_ISSUER are required")
	}
	cfg.Web = Web{
		URL:            getEnv("PUBLIC_WEB_URL", "http://localhost:5173"),
		CORSOrigins:    splitList(os.Getenv("WEB_CORS_ORIGINS")),
		ConnectLinkTTL: 24 * time.Hour,
	}
	for name, dest := range map[string]*time.Duration{
		"ADMIN_SESSION_TTL": &cfg.Admin.SessionTTL,
		"CONNECT_LINK_TTL":  &cfg.Web.ConnectLinkTTL,
	} {
		if err := envDuration(name, dest); err != nil {
			return Config{}, err
		}
		if *dest <= 0 {
			return Config{}, fmt.Errorf("%s must be positive", name)
		}
	}
	cfg.TrustedProxies = splitList(os.Getenv("HTTP_TRUSTED_PROXIES"))
	return cfg, nil
}

func loadLimits() (limits.Policy, error) {
	policy := limits.Default()
	var err error

	timezone := getEnv("LIMITS_TIMEZONE", "America/Lima")
	if policy.Location, err = time.LoadLocation(timezone); err != nil {
		return limits.Policy{}, fmt.Errorf("LIMITS_TIMEZONE: unknown time zone %q", timezone)
	}

	fields := []struct {
		name  string
		dest  *time.Duration
		intTo *int
	}{
		{name: "LIMITS_TRANSACTIONAL_INTERVAL", dest: &policy.TransactionalInterval},
		{name: "LIMITS_PROMOTIONAL_MIN_DELAY", dest: &policy.PromotionalMinDelay},
		{name: "LIMITS_PROMOTIONAL_MAX_DELAY", dest: &policy.PromotionalMaxDelay},
		{name: "LIMITS_TRANSACTIONAL_DAILY_CAP", intTo: &policy.TransactionalDailyCap},
		{name: "LIMITS_PROMOTIONAL_DAILY_CAP", intTo: &policy.PromotionalDailyCap},
		{name: "LIMITS_WARMUP_DAYS", intTo: &policy.WarmupDays},
	}
	for _, f := range fields {
		if f.dest != nil {
			err = envDuration(f.name, f.dest)
		} else {
			err = envInt(f.name, f.intTo)
		}
		if err != nil {
			return limits.Policy{}, err
		}
	}

	if err := policy.Validate(); err != nil {
		return limits.Policy{}, fmt.Errorf("invalid LIMITS_* settings: %w", err)
	}
	return policy, nil
}

func loadQueue() (Queue, error) {
	queue := Queue{MaxAttempts: 3, RetryBase: 30 * time.Second, NotConnectedRetry: 30 * time.Second, MaxAge: time.Hour}
	for name, dest := range map[string]*time.Duration{
		"QUEUE_RETRY_BASE":          &queue.RetryBase,
		"QUEUE_NOT_CONNECTED_RETRY": &queue.NotConnectedRetry,
		"QUEUE_MAX_AGE":             &queue.MaxAge,
	} {
		if err := envDuration(name, dest); err != nil {
			return Queue{}, err
		}
	}
	if err := envInt("QUEUE_MAX_ATTEMPTS", &queue.MaxAttempts); err != nil {
		return Queue{}, err
	}
	if queue.MaxAttempts < 1 || queue.RetryBase <= 0 || queue.NotConnectedRetry <= 0 || queue.MaxAge <= 0 {
		return Queue{}, errors.New("invalid QUEUE_* settings: values must be positive")
	}
	return queue, nil
}

func loadWebhook(env string) (Webhook, error) {
	webhook := Webhook{MaxAttempts: 6, Timeout: 10 * time.Second, RetryBase: 30 * time.Second, AllowPrivate: env == "development"}
	if err := envDuration("WEBHOOK_TIMEOUT", &webhook.Timeout); err != nil {
		return Webhook{}, err
	}
	if err := envDuration("WEBHOOK_RETRY_BASE", &webhook.RetryBase); err != nil {
		return Webhook{}, err
	}
	if err := envInt("WEBHOOK_MAX_ATTEMPTS", &webhook.MaxAttempts); err != nil {
		return Webhook{}, err
	}
	if value, ok := os.LookupEnv("WEBHOOK_ALLOW_PRIVATE"); ok && value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return Webhook{}, fmt.Errorf("WEBHOOK_ALLOW_PRIVATE: %q is not true or false", value)
		}
		webhook.AllowPrivate = parsed
	}
	if webhook.MaxAttempts < 1 || webhook.Timeout <= 0 || webhook.RetryBase <= 0 {
		return Webhook{}, errors.New("invalid WEBHOOK_* settings: values must be positive")
	}
	return webhook, nil
}

// envDuration overrides dest with the variable's value (e.g. "8s"), if set.
func envDuration(name string, dest *time.Duration) error {
	value, ok := os.LookupEnv(name)
	if !ok || value == "" {
		return nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("%s: %q is not a duration (examples: 500ms, 8s, 1h)", name, value)
	}
	*dest = parsed
	return nil
}

// envInt overrides dest with the variable's value, if set.
func envInt(name string, dest *int) error {
	value, ok := os.LookupEnv(name)
	if !ok || value == "" {
		return nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("%s: %q is not an integer", name, value)
	}
	*dest = parsed
	return nil
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
}

// splitList turns "a, b ,c" into ["a", "b", "c"], dropping blanks.
func splitList(value string) []string {
	var items []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}
