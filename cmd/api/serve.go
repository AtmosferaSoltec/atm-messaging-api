package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/AtmosferaSoltec/atm-messaging-api/internal/channel"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/channel/whatsapp"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/config"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/features/account"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/features/admin"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/features/connect"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/features/message"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/features/optout"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/features/session"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/features/tenant"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/features/webhook"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/platform/authtoken"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/platform/database"
	"github.com/AtmosferaSoltec/atm-messaging-api/internal/platform/httpserver"
)

func serve(ctx context.Context, log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	// One server per database: a WhatsApp device can only be connected from one place.
	releaseLock, err := database.AcquireInstanceLock(ctx, pool)
	if err != nil {
		return err
	}
	defer releaseLock()

	// The WhatsApp manager reports events to the session service, which is built
	// right after; events only start once a session is paired or restored.
	var sessions *session.Service
	var messages *message.Service
	routeEvent := func(evt channel.Event) {
		sessions.HandleEvent(evt)
		messages.HandleEvent(evt)
	}
	whatsappChannel, err := whatsapp.NewManager(ctx, cfg.DatabaseURL, routeEvent, log)
	if err != nil {
		return err
	}
	channels := []channel.Channel{whatsappChannel}

	tenants := tenant.NewService(tenant.NewRepository(pool))
	accounts := account.NewService(account.NewRepository(pool))

	webhookRepo := webhook.NewRepository(pool)
	webhookDispatcher := webhook.NewDispatcher(webhookRepo, tenants,
		webhook.NewHTTPClient(cfg.Webhook.Timeout, cfg.Webhook.AllowPrivate),
		webhook.DispatcherConfig{MaxAttempts: cfg.Webhook.MaxAttempts, RetryBase: cfg.Webhook.RetryBase}, log)
	webhooks := webhook.NewService(webhookRepo, tenants, webhookDispatcher, cfg.Webhook.AllowPrivate)

	sessions = session.NewService(session.NewRepository(pool), accounts, channels, webhooks, log)
	optOuts := optout.NewService(optout.NewRepository(pool), sessionOwnership{sessions})

	messageRepo := message.NewRepository(pool)
	resolver := sessionResolver{sessions}
	dispatcher := message.NewDispatcher(messageRepo, resolver, optOuts, channels, webhooks, message.DispatcherConfig{
		Policy:            cfg.Limits,
		MaxAttempts:       cfg.Queue.MaxAttempts,
		RetryBase:         cfg.Queue.RetryBase,
		NotConnectedRetry: cfg.Queue.NotConnectedRetry,
		MaxAge:            cfg.Queue.MaxAge,
	}, log)
	messages = message.NewService(messageRepo, resolver, optOuts, dispatcher, webhooks, log)

	if err := sessions.RestoreAll(ctx); err != nil {
		return err
	}
	if err := dispatcher.Start(ctx); err != nil {
		return err
	}
	if err := webhookDispatcher.Start(ctx); err != nil {
		return err
	}

	server, err := httpserver.New(cfg.HTTPPort, cfg.Env == "production", cfg.TrustedProxies, log)
	if err != nil {
		return err
	}
	engine := server.Engine()
	cors := httpserver.CORS(cfg.Web.CORSOrigins)

	// Health carries CORS headers too, so the web panel can show the service status.
	engine.GET("/health", cors, func(c *gin.Context) {
		if err := pool.Ping(c.Request.Context()); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "database_unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	verifier, err := authtoken.New(cfg.Auth.PublicKeyBase64, cfg.Auth.Issuer)
	if err != nil {
		return err
	}

	v1 := engine.Group("/v1", tenant.AuthMiddleware(tenants, verifier))
	session.RegisterRoutes(v1, session.NewHandler(sessions))
	message.RegisterRoutes(v1, message.NewHandler(messages))
	optout.RegisterRoutes(v1, optout.NewHandler(optOuts))
	webhook.RegisterRoutes(v1, webhook.NewHandler(webhooks))

	// Public links let a customer scan a QR code; the token in the URL is the credential.
	connectLinks := connect.NewService(connect.NewRepository(pool), sessions, cfg.Web.ConnectLinkTTL)
	connect.RegisterRoutes(engine.Group("/public", cors), connect.NewHandler(connectLinks))

	adminHandler := admin.NewHandler(tenants, sessions, messages, webhooks, connectLinks, cfg.Web.URL)
	admin.RegisterRoutes(engine.Group("/admin/v1", cors), adminHandler, verifier)

	runErr := server.Run(ctx)

	// Let the send in progress be recorded before dropping the connections.
	dispatcher.Stop()
	webhookDispatcher.Stop()
	whatsappChannel.Close()
	if runErr != nil {
		return fmt.Errorf("serve: %w", runErr)
	}
	return nil
}

// sessionResolver lets the message feature find sessions without importing the
// session feature. It serves both accepting a message (Resolve) and delivering
// it (Lookup).
type sessionResolver struct {
	sessions *session.Service
}

func (r sessionResolver) Resolve(ctx context.Context, tenantID uuid.UUID, accountRef string, ch channel.Type) (message.SessionInfo, error) {
	sess, err := r.sessions.Find(ctx, tenantID, accountRef, ch)
	if errors.Is(err, session.ErrNotFound) {
		return message.SessionInfo{}, message.ErrSessionNotFound
	}
	if err != nil {
		return message.SessionInfo{}, err
	}
	return message.SessionInfo{ID: sess.ID, Connected: sess.Status == session.StatusConnected}, nil
}

func (r sessionResolver) Lookup(ctx context.Context, sessionID uuid.UUID) (message.SessionDetails, error) {
	sess, err := r.sessions.Lookup(ctx, sessionID)
	if errors.Is(err, session.ErrNotFound) {
		return message.SessionDetails{}, message.ErrSessionNotFound
	}
	if err != nil {
		return message.SessionDetails{}, err
	}
	details := message.SessionDetails{Connected: sess.Status == session.StatusConnected}
	if sess.FirstConnectedAt != nil {
		details.FirstConnectedAt = *sess.FirstConnectedAt
	}
	return details, nil
}

// sessionOwnership lets the optout feature check that a session belongs to a tenant.
type sessionOwnership struct {
	sessions *session.Service
}

func (o sessionOwnership) Owns(ctx context.Context, tenantID, sessionID uuid.UUID) error {
	_, err := o.sessions.Get(ctx, tenantID, sessionID)
	if errors.Is(err, session.ErrNotFound) {
		return optout.ErrSessionNotFound
	}
	return err
}
