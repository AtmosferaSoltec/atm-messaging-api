// Package httpserver builds the Gin engine and runs it with graceful shutdown.
package httpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

const shutdownTimeout = 15 * time.Second

// Server wraps the Gin engine and the standard library HTTP server.
type Server struct {
	engine *gin.Engine
	port   string
	log    *slog.Logger
}

// New creates a Server with the base middlewares: request id, recover and logging.
// trustedProxies lists the reverse proxies whose X-Forwarded-For header is believed;
// with none, the client IP is the connection's address and cannot be spoofed.
func New(port string, production bool, trustedProxies []string, log *slog.Logger) (*Server, error) {
	if production {
		gin.SetMode(gin.ReleaseMode)
	}
	engine := gin.New()
	if err := engine.SetTrustedProxies(trustedProxies); err != nil {
		return nil, fmt.Errorf("set trusted proxies: %w", err)
	}
	engine.Use(RequestID(), Recover(log), AccessLog(log))
	return &Server{engine: engine, port: port, log: log}, nil
}

// Engine returns the router so features can register their routes.
func (s *Server) Engine() *gin.Engine { return s.engine }

// Run serves requests until ctx is cancelled, then shuts down gracefully.
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:              ":" + s.port,
		Handler:           s.engine,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		s.log.Info("http server listening", "port", s.port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown http server: %w", err)
	}
	return nil
}
