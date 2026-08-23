package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"temu-api-manager/internal/config"
	"temu-api-manager/internal/httpapi"
	"temu-api-manager/internal/inventory"
	"temu-api-manager/internal/oms"
	"temu-api-manager/internal/service"
	"temu-api-manager/internal/shopregistry"
	"temu-api-manager/internal/store"
	"temu-api-manager/internal/temu"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, logger); err != nil {
		logger.Error("Temu service stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := requireLoopback(cfg.Listen); err != nil {
		return err
	}
	registry, err := shopregistry.New(ctx, cfg.DatabaseURL, cfg.ShopCredentialsKeyFile)
	if err != nil {
		return err
	}
	defer registry.Close()
	if err := registry.Ensure(ctx, shopregistry.Shop{
		Code: cfg.StoreCode, Name: cfg.StoreName, SchemaName: "temu_panda_homes",
		AppKey: cfg.AppKey, AppSecret: cfg.AppSecret, AccessToken: cfg.AccessToken, Enabled: true,
	}); err != nil {
		return err
	}
	shops, err := registry.List(ctx)
	if err != nil {
		return err
	}
	handlers := make(map[string]http.Handler, len(shops))
	shopInfo := make([]httpapi.ShopInfo, 0, len(shops))
	for _, shop := range shops {
		if !shop.Enabled {
			continue
		}
		shopInfo = append(shopInfo, httpapi.ShopInfo{Code: shop.Code, Name: shop.Name, Default: shop.Code == cfg.StoreCode})
		destination, destinationErr := store.NewPostgresForShop(ctx, cfg.DatabaseURL, shop.SchemaName, shop.Code)
		if destinationErr != nil {
			return fmt.Errorf("initialize shop %s database: %w", shop.Code, destinationErr)
		}
		defer destination.Close()
		if err := destination.Migrate(ctx); err != nil {
			return fmt.Errorf("migrate shop %s: %w", shop.Code, err)
		}
		temuClient := temu.NewClient(cfg.APIBaseURL, shop.AppKey, shop.AppSecret, shop.AccessToken, cfg.RequestTimeout)
		if err := temuClient.SetRequestInterval(cfg.APIRequestInterval); err != nil {
			return err
		}
		if err := temuClient.SetShipmentCreateConcurrency(cfg.ShipmentCreateConcurrency); err != nil {
			return err
		}
		if err := temuClient.SetDocumentProxyBaseURL(cfg.DocumentProxyBaseURL); err != nil {
			return err
		}
		shopLogger := logger.With("shop_code", shop.Code, "shop_name", shop.Name)
		manager := service.NewForShop(
			destination,
			temuClient,
			inventory.NewClient(cfg.WarehouseDecisionURL, cfg.RequestTimeout),
			oms.NewClient(cfg.XLWMSOutboundURL, cfg.RequestTimeout),
			shop.Code,
			shop.Name,
			cfg.QuoteLifetime,
			shopLogger,
		)
		handlers[shop.Code] = httpapi.New(manager, cfg.OperationKey, shop.Code, shop.Name, cfg.StaticRoot, cfg.RequestTimeout, shopLogger)
		go backgroundSync(ctx, manager, cfg.OrderSyncInterval, cfg.RequestTimeout, shopLogger)
		go backgroundOMSQuery(ctx, manager, cfg.OMSQueryInterval, cfg.RequestTimeout, shopLogger)
		go backgroundPickupAudits(ctx, manager, shop.Code, shop.Name, cfg.PickupAuditInterval, cfg.RequestTimeout*10, shopLogger)
		go backgroundBulkFulfillment(ctx, manager, cfg.FulfillmentInterval, cfg.RequestTimeout, cfg.FulfillmentConcurrency, shopLogger)
		go backgroundAutoFulfillment(ctx, manager, cfg.FulfillmentInterval, cfg.RequestTimeout, cfg.FulfillmentConcurrency, shopLogger)
	}
	if _, ok := handlers[cfg.StoreCode]; !ok {
		return errors.New("default Temu shop is not enabled")
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Listen, err)
	}
	server := &http.Server{Handler: httpapi.NewShopRouter(cfg.StoreCode, shopInfo, handlers), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	errorsChannel := make(chan error, 1)
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errorsChannel <- serveErr
		}
	}()
	logger.Info("Temu Go service started", "default_shop", cfg.StoreCode, "shop_count", len(handlers), "listen", listener.Addr().String(), "order_sync_interval", cfg.OrderSyncInterval.String(), "oms_query_interval", cfg.OMSQueryInterval.String(), "fulfillment_interval", cfg.FulfillmentInterval.String(), "fulfillment_concurrency", cfg.FulfillmentConcurrency, "shipment_create_concurrency", cfg.ShipmentCreateConcurrency, "api_request_interval", cfg.APIRequestInterval.String())
	select {
	case <-ctx.Done():
		logger.Info("Temu service shutdown requested")
	case serveErr := <-errorsChannel:
		return serveErr
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}

func backgroundPickupAudits(ctx context.Context, manager *service.Service, shopCode, shopName string, interval, timeout time.Duration, logger *slog.Logger) {
	run := func() {
		auditCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		result, err := manager.SyncPendingPickupAudits(auditCtx, shopCode, shopName)
		if err != nil {
			logger.Warn("pending-pickup audit snapshot failed", "fetched", result.Fetched, "error", err)
		} else {
			logger.Info("pending-pickup audit snapshot synced", "orders", result.Synced)
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func backgroundSync(ctx context.Context, manager *service.Service, interval, timeout time.Duration, logger *slog.Logger) {
	run := func() {
		warehouseCtx, cancelWarehouses := context.WithTimeout(ctx, timeout)
		if _, _, err := manager.SyncWarehouses(warehouseCtx); err != nil {
			logger.Warn("Temu warehouse sync failed", "error", err)
		}
		cancelWarehouses()

		orderCtx, cancelOrders := context.WithTimeout(ctx, timeout)
		status, err := manager.SyncOrders(orderCtx)
		cancelOrders()
		if err != nil {
			logger.Warn("Temu order sync failed; continuing warehouse classification from stored orders", "error", err)
		} else {
			logger.Info("Temu order sync completed", "orders", status.FetchedOrders, "lines", status.FetchedLines)

			detailCtx, cancelDetails := context.WithTimeout(ctx, timeout)
			details, detailErr := manager.SyncOrderDetails(detailCtx, 10)
			cancelDetails()
			if detailErr != nil {
				logger.Warn("Temu order detail sync incomplete", "completed", details, "error", detailErr)
			} else if details > 0 {
				logger.Info("Temu order detail sync completed", "details", details)
			}
		}

		classificationCtx, cancelClassification := context.WithTimeout(ctx, timeout*4)
		totalClassification := service.WarehouseClassificationStats{}
		var classificationErr error
		for {
			batch, batchErr := manager.ClassifyWarehouseQueue(classificationCtx, 500)
			totalClassification.Checked += batch.Checked
			totalClassification.Eligible += batch.Eligible
			totalClassification.Manual += batch.Manual
			totalClassification.Failed += batch.Failed
			if batchErr != nil {
				classificationErr = batchErr
				break
			}
			if batch.Checked < 500 {
				break
			}
		}
		cancelClassification()
		if classificationErr != nil {
			logger.Warn("Temu warehouse classification incomplete", "checked", totalClassification.Checked, "eligible", totalClassification.Eligible, "manual", totalClassification.Manual, "failed", totalClassification.Failed, "error", classificationErr)
		} else {
			logger.Info("Temu warehouse classification completed", "checked", totalClassification.Checked, "eligible", totalClassification.Eligible, "manual", totalClassification.Manual, "failed", totalClassification.Failed)
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func backgroundBulkFulfillment(ctx context.Context, manager *service.Service, interval, timeout time.Duration, concurrency int, logger *slog.Logger) {
	run := func() {
		workerCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		processed, err := manager.ProcessBulkFulfillment(workerCtx, concurrency)
		if err != nil {
			logger.Warn("bulk fulfillment stopped", "error", err)
		} else if processed {
			logger.Info("bulk fulfillment advanced", "concurrency", concurrency)
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func backgroundAutoFulfillment(ctx context.Context, manager *service.Service, interval, timeout time.Duration, concurrency int, logger *slog.Logger) {
	run := func() {
		workerCtx, cancel := context.WithTimeout(ctx, timeout*4)
		defer cancel()
		processed, err := manager.ProcessAutoFulfillments(workerCtx, interval, concurrency)
		if err != nil {
			logger.Warn("automatic fulfillment cycle incomplete", "processed", processed, "error", err)
		} else if processed > 0 {
			logger.Info("automatic fulfillment cycle completed", "processed", processed)
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func backgroundOMSQuery(ctx context.Context, manager *service.Service, interval, timeout time.Duration, logger *slog.Logger) {
	run := func() {
		queryCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		checked, err := manager.QueryPendingOMSSync(queryCtx, interval, 20)
		if err != nil {
			logger.Warn("XLWMS read-only sync query incomplete", "checked", checked, "error", err)
		} else if checked > 0 {
			logger.Info("XLWMS read-only sync query completed", "checked", checked)
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func requireLoopback(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid TEMU_LISTEN: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("TEMU_LISTEN must use a loopback address")
	}
	return nil
}
