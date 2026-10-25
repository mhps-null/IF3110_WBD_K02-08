package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/access"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/admin"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/auth"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/config"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/connections"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/contract"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/dbdriver/mysql"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/dbdriver/postgres"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/migration"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/schemadiff"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/session"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/viewer"
	"github.com/mhps-null/IF3110_WBD_K02-08/internal/web"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid application configuration", "reason", err.Error())
		os.Exit(1)
	}

	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		logger.Error("cannot initialize metadata database; check DATABASE_URL")
		os.Exit(1)
	}
	defer db.Close()
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	pingCtx, cancelPing := context.WithTimeout(context.Background(), 5*time.Second)
	err = db.PingContext(pingCtx)
	cancelPing()
	if err != nil {
		// Error driver tidak dicetak karena dapat memuat detail koneksi.
		logger.Error("cannot connect to metadata database; check database availability and DATABASE_URL")
		os.Exit(1)
	}

	renderer, err := web.NewRenderer("web/templates")
	if err != nil {
		logger.Error("cannot initialize HTML templates")
		os.Exit(1)
	}
	cfgSession := session.DefaultConfig()
	sessions := session.NewSQLStore(db, cfgSession)
	flashManager, err := web.NewFlashManager(cfg.SessionSecret, cfg.SessionCookieSecure)
	if err != nil {
		logger.Error("cannot initialize flash message manager")
		os.Exit(1)
	}
	authHandler := auth.NewHandler(auth.NewSQLUsers(db), sessions, renderer, cfg.SessionCookieSecure, cfgSession, logger, flashManager)

	mux := http.NewServeMux()
	checker := access.NewChecker(db)
	guards := access.NewGuards(access.GuardsConfig{Checker: checker})
	adminHandler := admin.NewHandler(admin.NewStore(db), sessions, nil, logger)
	adminHandler.Routes(mux, authHandler.Authenticate, guards.Admin)

	// Database connection passwords are encrypted at rest. Refuse startup rather
	// than silently store or interpret credentials without the configured key.
	encryptionKey, err := connections.LoadEncryptionKey()
	if err != nil {
		logger.Error("invalid database encryption configuration; check DB_ENCRYPTION_KEY")
		os.Exit(1)
	}
	connectionRepo, err := connections.NewSQLRepository(db, encryptionKey)
	if err != nil {
		logger.Error("cannot initialize database connection repository")
		os.Exit(1)
	}
	poolManager := connections.NewPoolManager(connectionRepo, "")
	defer func() {
		if err := poolManager.CloseAll(); err != nil {
			logger.Error("failed to close database connection pools")
		}
	}()

	connectionHandler := connections.NewHandler(connectionRepo, poolManager, admin.NewTemplateRenderer("web/templates"), guards)
	connectionHandler.RegisterRoutesWithAuth(mux, authHandler.Authenticate)

	introspectors := map[contract.Engine]contract.Introspector{
		contract.EnginePostgres: postgres.NewIntrospector(),
		contract.EngineMySQL:    mysql.NewIntrospector(),
		contract.EngineMariaDB:  mysql.NewIntrospector(),
	}
	viewerHandler := viewer.NewMultiEngineHandler(poolManager, introspectors, checker, viewer.NewHTMLRenderer("web/templates"), guards)
	viewerHandler.RegisterRoutesWithAuth(mux, authHandler.Authenticate)

	schemaDiffHandler := schemadiff.NewHandler(
		checker,
		introspectors,
		poolManager,
		admin.NewTemplateRenderer("web/templates"),
		logger,
	)
	schemaDiffHandler.Routes(mux, authHandler.Authenticate, guards.ViewDB)

	migrationHandler := migration.NewHandler(
		migration.NewService(migration.NewSQLRepository(db), poolManager),
		migration.NewHTMLRenderer("web/templates"),
		guards,
	)
	migrationHandler.RegisterRoutesWithAuth(mux, authHandler.Authenticate)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /login", authHandler.LoginPage)
	mux.HandleFunc("POST /login", authHandler.Login)
	mux.Handle("GET /{$}", authHandler.Authenticate(http.HandlerFunc(authHandler.RenderHome)))
	mux.Handle("POST /logout", authHandler.Authenticate(http.HandlerFunc(authHandler.Logout)))
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("web/static"))))

	var handler http.Handler = mux
	handler = web.NewCSRFMiddleware(cfg.SessionSecret, cfg.SessionCookieSecure).Wrap(handler)
	handler = web.RequestLogger(logger, handler)
	handler = web.SecurityHeaders(handler)

	server := &http.Server{
		Addr: cfg.Addr, Handler: handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serverErr := make(chan error, 1)
	go func() {
		logger.Info("server starting", "addr", cfg.Addr, "environment", cfg.Env)
		serverErr <- server.ListenAndServe()
	}()

	stopCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case <-stopCtx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed")
		}
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server stopped unexpectedly")
		}
	}
}
