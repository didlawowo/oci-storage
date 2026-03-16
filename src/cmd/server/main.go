package main

import (
	"oci-storage/config"
	"oci-storage/pkg/coordination"
	"oci-storage/pkg/handlers"
	"oci-storage/pkg/interfaces"
	middleware "oci-storage/pkg/middlewares"
	ociRedis "oci-storage/pkg/redis"
	service "oci-storage/pkg/services"
	"oci-storage/pkg/storage"
	"oci-storage/pkg/utils"
	"oci-storage/pkg/version"
	"path/filepath"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/template/html/v2"
	"github.com/sirupsen/logrus"
)

// setupBackend creates the storage backend (local filesystem or S3) based on config
func setupBackend(cfg *config.Config, log *utils.Logger) storage.Backend {
	if cfg.S3.Enabled {
		log.WithFields(logrus.Fields{
			"endpoint": cfg.S3.Endpoint,
			"bucket":   cfg.S3.Bucket,
			"region":   cfg.S3.Region,
		}).Info("Initializing S3 storage backend")

		localTempDir := filepath.Join(cfg.Storage.Path, "temp")
		backend, err := storage.NewS3Backend(cfg.S3, localTempDir)
		if err != nil {
			log.WithError(err).Fatal("Failed to initialize S3 backend")
		}
		return backend
	}

	log.WithField("path", cfg.Storage.Path).Info("Using local filesystem storage backend")
	return storage.NewLocalBackend(cfg.Storage.Path)
}

// setupCoordination creates distributed coordination primitives.
// With Redis: distributed locks + upload tracking + scan dedup across replicas.
// Without Redis: noop implementations (single-replica mode).
// Returns a cleanup function that must be deferred to close connections.
func setupCoordination(cfg *config.Config, log *utils.Logger) (coordination.LockManager, coordination.UploadTracker, coordination.ScanTracker, func()) {
	if cfg.Redis.Enabled {
		client, err := ociRedis.NewClient(cfg.Redis, log)
		if err != nil {
			log.WithError(err).Fatal("Failed to connect to Redis (required for multi-replica mode)")
		}
		cleanup := func() {
			log.Info("Closing Redis connection")
			client.Close()
		}
		// Client implements LockManager, UploadTracker, and ScanTracker
		return client, client, client, cleanup
	}

	log.Info("Redis disabled - running in single-replica mode (no distributed coordination)")
	return &coordination.NoopLockManager{}, &coordination.NoopUploadTracker{}, &coordination.NoopScanTracker{}, func() {}
}

// setupServices initialise et configure tous les services
func setupServices(cfg *config.Config, log *utils.Logger, pm *utils.PathManager, backend storage.Backend, locker coordination.LockManager, scanTracker coordination.ScanTracker) (interfaces.ChartServiceInterface, interfaces.ImageServiceInterface, interfaces.IndexServiceInterface, interfaces.ProxyServiceInterface, *service.BackupService, interfaces.ScanServiceInterface) {

	tmpChartService := service.NewChartService(cfg, log, pm, backend, nil)
	indexService := service.NewIndexService(cfg, log, pm, backend, tmpChartService, locker)
	finalChartService := service.NewChartService(cfg, log, pm, backend, indexService)
	imageService := service.NewImageService(cfg, log, pm, backend)
	backupService, err := service.NewBackupService(cfg, log)
	if err != nil {
		log.WithFunc().WithError(err).Fatal("Failed to initialize backup service")
	}

	// Initialize proxy service if enabled
	var proxyService interfaces.ProxyServiceInterface
	if cfg.Proxy.Enabled {
		proxyService = service.NewProxyService(cfg, log, pm, backend)
		log.Info("Proxy/cache service enabled")
	}

	// Initialize scan service if enabled
	var scanService interfaces.ScanServiceInterface
	if cfg.Trivy.Enabled {
		scanService = service.NewScanService(cfg, log, pm, backend, locker, scanTracker)
		log.Info("Trivy scan service enabled")
	}

	return finalChartService, imageService, indexService, proxyService, backupService, scanService
}

// setupHandlers initialise tous les handlers
func setupHandlers(
	chartService interfaces.ChartServiceInterface,
	imageService interfaces.ImageServiceInterface,
	_ interfaces.IndexServiceInterface,
	proxyService interfaces.ProxyServiceInterface,
	scanService interfaces.ScanServiceInterface,
	pathManager *utils.PathManager,
	backend storage.Backend,
	uploadTracker coordination.UploadTracker,
	cfg *config.Config,
	backupService *service.BackupService,
	log *utils.Logger,

) (*handlers.HelmHandler, *handlers.ImageHandler, *handlers.OCIHandler, *handlers.ConfigHandler, *handlers.IndexHandler, *handlers.BackupHandler, *handlers.CacheHandler, *handlers.GCHandler, *handlers.ScanHandler) {
	helmHandler := handlers.NewHelmHandler(chartService, pathManager, log, backend)
	imageHandler := handlers.NewImageHandler(imageService, proxyService, pathManager, log)
	ociHandler := handlers.NewOCIHandler(chartService, imageService, proxyService, scanService, cfg, log, pathManager, backend, uploadTracker)
	configHandler := handlers.NewConfigHandler(cfg, log)
	indexHandler := handlers.NewIndexHandler(chartService, pathManager, log, backend)
	backupHandler := handlers.NewBackupHandler(backupService, log, cfg)
	cacheHandler := handlers.NewCacheHandler(proxyService, log)

	// GC handler - needs concrete ProxyService for GCService
	var gcHandler *handlers.GCHandler
	if proxyService != nil {
		// Type assert to get concrete ProxyService
		if ps, ok := proxyService.(*service.ProxyService); ok {
			gcService := service.NewGCService(cfg, pathManager, backend, ps, log)
			gcHandler = handlers.NewGCHandler(gcService, log)
		}
	}

	// Scan handler
	var scanHandler *handlers.ScanHandler
	if scanService != nil {
		scanHandler = handlers.NewScanHandler(scanService, log)
	}

	return helmHandler, imageHandler, ociHandler, configHandler, indexHandler, backupHandler, cacheHandler, gcHandler, scanHandler
}

func setupHTTPServer(app *fiber.App, log *utils.Logger) {

	log.WithFunc().Info("🚀 Application starting")

	if err := app.Listen(":3030"); err != nil {
		log.WithFunc().WithError(err).Fatal("HTTP Server failed")
	}
}

func main() {
	// Configuration - load first to get logging settings
	cfg, err := config.LoadConfig("config/config.yaml")
	if err != nil {
		// Use a basic logger for startup errors
		logrus.WithError(err).Fatal("Failed to load configuration")
	}

	// Logger setup from config
	logConfig := utils.Config{
		LogLevel:  cfg.Logging.Level,
		LogFormat: cfg.Logging.Format,
		Pretty:    true,
	}
	// Default values if not set in config
	if logConfig.LogLevel == "" {
		logConfig.LogLevel = "info"
	}
	if logConfig.LogFormat == "" {
		logConfig.LogFormat = "text"
	}
	log := utils.NewLogger(logConfig)

	// Log version info at startup
	log.WithFields(logrus.Fields{
		"version": version.Version,
		"commit":  version.Commit,
	}).Info("oci storage starting")

	if err := config.LoadAuthFromFile(cfg); err != nil {
		log.WithError(err).Fatal("Failed to load auth configuration")
	}

	// Storage backend (local or S3)
	backend := setupBackend(cfg, log)

	// Distributed coordination (Redis or noop)
	locker, uploadTracker, scanTracker, coordCleanup := setupCoordination(cfg, log)
	defer coordCleanup()

	// PathManager
	pathManager := utils.NewPathManager(cfg.Storage.Path, log)

	// Services
	chartService, imageService, indexService, proxyService, backupService, scanService := setupServices(cfg, log, pathManager, backend, locker, scanTracker)

	// Ensure index.yaml exists at startup
	if err := indexService.EnsureIndexExists(); err != nil {
		log.WithError(err).Error("Failed to ensure index.yaml exists")
	}

	// Handlers
	helmHandler, imageHandler, ociHandler, configHandler, indexHandler, backupHandler, cacheHandler, gcHandler, scanHandler := setupHandlers(
		chartService,
		imageService,
		indexService,
		proxyService,
		scanService,
		pathManager,
		backend,
		uploadTracker,
		cfg,
		backupService,
		log,
	)

	// Fiber app configuration
	app := fiber.New(fiber.Config{
		AppName:           "oci storage",
		Prefork:           false,
		CaseSensitive:     true,
		StrictRouting:     true,
		ServerHeader:      "oci storage",
		BodyLimit:         10 * 1024 * 1024 * 1024, // 10GB for large Docker image layers (ML models, etc.)
		StreamRequestBody: true,                    // Enable streaming for large uploads
		ReadTimeout:       30 * time.Minute,        // Allow 30 minutes for large blob uploads (ML models)
		WriteTimeout:      30 * time.Minute,        // Allow 30 minutes for large blob downloads
		IdleTimeout:       2 * time.Minute,         // Close idle connections after 2 minutes
		Views:             html.New("./views", ".html"),

		ErrorHandler: func(c *fiber.Ctx, err error) error {
			log.WithFields(logrus.Fields{
				"path":   c.Path(),
				"method": c.Method(),
				"error":  err.Error(),
			}).Error("Error handling request")
			return c.Status(500).SendString("Internal Server Error")
		},
	})

	// Middleware pour le logging
	app.Use(func(c *fiber.Ctx) error {
		// Health check en debug pour éviter le spam
		if c.Path() == "/health" {
			log.Debug("Health check")
			return c.Next()
		}

		log.WithFields(logrus.Fields{
			"path":   c.Path(),
			"method": c.Method(),
			"route":  c.Route().Path,
			"params": c.AllParams(),
		}).Info("Incoming request")

		return c.Next()
	})
	// app.Use(middleware.HTTPSRedirect(log))

	app.Static("/static", "./views/static")

	// Routes
	app.Get("/favicon.ico", func(c *fiber.Ctx) error {
		return c.SendFile("./views/static/ico.png")
	})
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.SendString("OK")
	})
	// Créer le middleware d'authentification
	authMiddleware := middleware.NewAuthMiddleware(cfg, log)
	if !cfg.Auth.IsEnabled() {
		log.Warn("Authentication is DISABLED - all /v2/ write operations are open")
	}

	// Appliquer le middleware aux routes OCI qui nécessitent une authentification
	ociGroup := app.Group("/v2")
	ociGroup.Use(authMiddleware.Authenticate())
	// log.WithField("config", *cfg).Info("Configuration loaded")
	log.WithField("backup", cfg.Backup).Info("Backup configuration")
	// Routes Portal Interface
	app.Get("/", helmHandler.DisplayHome)
	app.Get("/backup/status", backupHandler.GetBackupStatus)

	// Helm Chart routes
	// IMPORTANT: /chart/:name/versions MUST come before /chart/:name/:version to avoid "versions" being captured as a version
	app.Get("/chart/:name/versions", helmHandler.GetChartVersions)
	app.Get("/chart/:name/:version/details", helmHandler.DisplayChartDetails)
	app.Delete("/chart/:name/:version", helmHandler.DeleteChart)
	app.Post("/chart", helmHandler.UploadChart)
	app.Get("/config", configHandler.GetConfig)
	app.Get("/chart/:name/:version", helmHandler.DownloadChart)
	app.Get("/index.yaml", indexHandler.GetIndex)
	app.Get("/charts", helmHandler.ListCharts)

	// Docker Image routes
	app.Get("/images", imageHandler.ListImages)
	// Deep nested paths for proxy images (e.g., /image/proxy/docker.io/nginx/alpine/details)
	// Use All() with wildcard to catch all /image/* paths
	app.All("/image/*", func(c *fiber.Ctx) error {
		if c.Method() == "DELETE" {
			return imageHandler.HandleImageDeleteWildcard(c)
		}
		return imageHandler.HandleImageWildcard(c)
	})

	// Routes Backup
	app.Post("/backup", backupHandler.HandleBackup)
	app.Post("/restore", backupHandler.HandleRestore)

	// Cache/Proxy management routes
	app.Get("/cache/status", cacheHandler.GetCacheStatus)
	app.Get("/cache/images", cacheHandler.ListCachedImages)
	app.Delete("/cache/image/*", cacheHandler.DeleteCachedImageWildcard)
	app.Post("/cache/purge", cacheHandler.PurgeCache)

	// Garbage collection routes
	if gcHandler != nil {
		app.Post("/gc", gcHandler.RunGC)
		app.Get("/gc/stats", gcHandler.GetStats)
	}

	// Scan / Security Gate routes
	if scanHandler != nil {
		app.Get("/api/scan/pending", scanHandler.GetPending)
		app.Get("/api/scan/summary", scanHandler.GetSummary)
		app.Get("/api/scan/all", scanHandler.ListAll)
		app.Get("/api/scan/report/:digest", scanHandler.GetReport)
		app.Get("/api/scan/status/:digest", scanHandler.GetScanStatus)
		app.Post("/api/scan/trigger", scanHandler.TriggerScan)
		app.Post("/api/scan/approve/:digest", scanHandler.Approve)
		app.Post("/api/scan/deny/:digest", scanHandler.Deny)
		app.Delete("/api/scan/decision/:digest", scanHandler.DeleteDecision)
	}

	// Routes OCI - support nested paths like charts/myapp or images/myapp
	ociGroup.Get("/", ociHandler.HandleOCIAPI)
	ociGroup.Get("/_catalog", ociHandler.HandleCatalog)
	// Single segment names (legacy)
	ociGroup.Get("/:name/tags/list", ociHandler.HandleListTags)
	ociGroup.Head("/:name/manifests/:reference", ociHandler.HandleManifest)
	ociGroup.Get("/:name/manifests/:reference", ociHandler.HandleManifest)
	ociGroup.Put("/:name/manifests/:reference", ociHandler.PutManifest)
	ociGroup.Put("/:name/blobs/:digest", ociHandler.PutBlob)
	ociGroup.Post("/:name/blobs/uploads/", ociHandler.PostUpload)
	ociGroup.Patch("/:name/blobs/uploads/:uuid", ociHandler.PatchBlob)
	ociGroup.Put("/:name/blobs/uploads/:uuid", ociHandler.CompleteUpload)
	ociGroup.Head("/:name/blobs/:digest", ociHandler.HeadBlob)
	ociGroup.Get("/:name/blobs/:digest", ociHandler.GetBlob)
	// Nested paths (charts/name or images/name)
	ociGroup.Get("/:namespace/:name/tags/list", ociHandler.HandleListTagsNested)
	ociGroup.Head("/:namespace/:name/manifests/:reference", ociHandler.HandleManifestNested)
	ociGroup.Get("/:namespace/:name/manifests/:reference", ociHandler.HandleManifestNested)
	ociGroup.Put("/:namespace/:name/manifests/:reference", ociHandler.PutManifestNested)
	ociGroup.Put("/:namespace/:name/blobs/:digest", ociHandler.PutBlobNested)
	ociGroup.Post("/:namespace/:name/blobs/uploads/", ociHandler.PostUploadNested)
	ociGroup.Patch("/:namespace/:name/blobs/uploads/:uuid", ociHandler.PatchBlobNested)
	ociGroup.Put("/:namespace/:name/blobs/uploads/:uuid", ociHandler.CompleteUploadNested)
	ociGroup.Head("/:namespace/:name/blobs/:digest", ociHandler.HeadBlobNested)
	ociGroup.Get("/:namespace/:name/blobs/:digest", ociHandler.GetBlobNested)

	// Proxy paths with 3 segments (proxy/registry/image)
	ociGroup.Get("/:ns1/:ns2/:name/tags/list", ociHandler.HandleListTagsDeepNested)
	ociGroup.Head("/:ns1/:ns2/:name/manifests/:reference", ociHandler.HandleManifestDeepNested)
	ociGroup.Get("/:ns1/:ns2/:name/manifests/:reference", ociHandler.HandleManifestDeepNested)
	ociGroup.Head("/:ns1/:ns2/:name/blobs/:digest", ociHandler.HeadBlobDeepNested)
	ociGroup.Get("/:ns1/:ns2/:name/blobs/:digest", ociHandler.GetBlobDeepNested)

	// Proxy paths with 4 segments (proxy/registry/namespace/image) - e.g., proxy/docker.io/library/nginx
	ociGroup.Get("/:ns1/:ns2/:ns3/:name/tags/list", ociHandler.HandleListTagsDeepNested4)
	ociGroup.Head("/:ns1/:ns2/:ns3/:name/manifests/:reference", ociHandler.HandleManifestDeepNested4)
	ociGroup.Get("/:ns1/:ns2/:ns3/:name/manifests/:reference", ociHandler.HandleManifestDeepNested4)
	ociGroup.Head("/:ns1/:ns2/:ns3/:name/blobs/:digest", ociHandler.HeadBlobDeepNested4)
	ociGroup.Get("/:ns1/:ns2/:ns3/:name/blobs/:digest", ociHandler.GetBlobDeepNested4)

	// Proxy paths with 5 segments (proxy/registry/org/repo/image) - e.g., proxy/ghcr.io/actions/gha-runner-scale-set-controller
	ociGroup.Get("/:ns1/:ns2/:ns3/:ns4/:name/tags/list", ociHandler.HandleListTagsDeepNested5)
	ociGroup.Head("/:ns1/:ns2/:ns3/:ns4/:name/manifests/:reference", ociHandler.HandleManifestDeepNested5)
	ociGroup.Get("/:ns1/:ns2/:ns3/:ns4/:name/manifests/:reference", ociHandler.HandleManifestDeepNested5)
	ociGroup.Head("/:ns1/:ns2/:ns3/:ns4/:name/blobs/:digest", ociHandler.HeadBlobDeepNested5)
	ociGroup.Get("/:ns1/:ns2/:ns3/:ns4/:name/blobs/:digest", ociHandler.GetBlobDeepNested5)

	// Démarrage du serveur
	port := ":3030"
	log.WithField("port", port).Info("Starting server")

	setupHTTPServer(app, log)
}
