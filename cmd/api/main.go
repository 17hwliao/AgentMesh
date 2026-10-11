package main

import (
	"agentmesh/internal/billing"
	"agentmesh/internal/configenv"
	"agentmesh/internal/semanticcache"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"agentmesh/internal/admin"         //后台管理接口(管理员路由,用量查询)
	"agentmesh/internal/auth"          //鉴权,密钥存储,身份校验中间件
	"agentmesh/internal/embedding"     //向量嵌入模型服务(文本的向量化接口)
	"agentmesh/internal/gateway"       //核心的AI网关,租户路由分发,请求流转核心
	"agentmesh/internal/observability" //可观测性：埋点、指标记录、请求统计
	"agentmesh/internal/ratelimit"     //全局限流运行时、流量控制
	"agentmesh/internal/reservation"   //资源配额 / 预留系统，限制租户调用额度
	"agentmesh/internal/runtime"       //AI 后端提供器管理（mock/ark/ollama 大模型）
	"agentmesh/internal/telemetry"
	"agentmesh/internal/tenant"     //多租户路由解析，租户隔离逻辑
	"agentmesh/internal/usagekafka" //调用用量统计，Kafka 上报 + MySQL 持久化
)

const (
	serverReadHeaderTimeout = 5 * time.Second  //读取请求头
	serverReadTimeout       = 15 * time.Second //完整读取请求体
	serverIdleTimeout       = 60 * time.Second //空闲
)

// 创建统一的 Http Server
func newHTTPServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: serverReadHeaderTimeout,
		ReadTimeout:       serverReadTimeout,
		IdleTimeout:       serverIdleTimeout,
		MaxHeaderBytes:    16 << 10,
		// 写超时（WriteTimeout）保持置零，避免合法的服务器发送事件（SSE）长流被固定的绝对时钟超时强制断开。
		//数据流的终止边界仅由服务提供方设定的截止时间与客户端主动取消操作决定
		//防止多重重新编写重试的参数
		WriteTimeout: 0,
	}
}

func main() {
	if err := configenv.LoadSecretFiles(); err != nil {
		log.Fatal("secret_configuration_failed")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	flags := flag.NewFlagSet("api", flag.ExitOnError)
	address := flags.String("addr", "127.0.0.1:18080", "local listen address (must be 127.0.0.1:PORT)")
	allowContainerListen := flags.Bool("allow-container-listen", false, "allow 0.0.0.0:PORT only for container-local binding") //容器开启后可以提供外部访问
	providerOrder := flags.String("providers", "mock", "provider route: mock or comma-separated ark,ollama,compatible")
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: %s [--addr 127.0.0.1:PORT]\n", os.Args[0])
		flags.PrintDefaults()
	}
	_ = flags.Parse(os.Args[1:])
	// 校验监听地址防止消息外泄
	if err := validateServerAddress(*address, *allowContainerListen); err != nil {
		log.Fatal(err)
	}
	// 限流组件
	rateRuntime, err := ratelimit.OpenConfiguredRuntime(os.Getenv)
	if err != nil {
		if code, ok := ratelimit.IsConfigurationError(err); ok {
			log.Fatal(code)
		}
		log.Fatal("rate_limit_configuration_invalid")
	}
	defer rateRuntime.Close()

	logicalProviders, err := runtime.Selection(*providerOrder)
	if err != nil {
		if code, ok := runtime.IsConfigurationError(err); ok {
			log.Fatal(code)
		}
		log.Fatal("provider_selection_invalid")
	}
	configuredStore, err := auth.OpenConfiguredRuntime(os.Getenv)
	if err != nil {
		if code, ok := auth.IsConfigurationError(err); ok {
			log.Fatal(code)
		}
		log.Fatal("auth_configuration_invalid")
	}
	defer configuredStore.Close()
	store := configuredStore.Store
	providers, err := runtime.Build(*providerOrder, os.Getenv)
	if err != nil {
		if code, ok := runtime.IsConfigurationError(err); ok {
			log.Fatal(code)
		}
		log.Fatal("provider_configuration_invalid")
	}
	embeddingProviders, err := embedding.Build(os.Getenv)
	if err != nil {
		if code, ok := embedding.IsConfigurationError(err); ok {
			log.Fatal(code)
		}
		log.Fatal("embedding_provider_configuration_invalid")
	}
	startupContext, cancelStartup := context.WithTimeout(context.Background(), 2*time.Second)
	var inactiveProviders []string
	if !(len(logicalProviders) == 1 && logicalProviders[0] == "mock") {
		for _, known := range []string{"ark", "ollama", "compatible"} {
			active := false
			for _, selected := range logicalProviders {
				if selected == known {
					active = true
				}
			}
			if !active {
				inactiveProviders = append(inactiveProviders, known)
			}
		}
	}
	resolver, err := tenant.NewResolverWithInactive(startupContext, store, logicalProviders, providers, inactiveProviders)
	cancelStartup()
	if err != nil {
		log.Fatal("tenant_route_configuration_invalid")
	}
	reservationGate, cleanupReservation, err := reservation.OpenConfiguredCoordinator(os.Getenv)
	if err != nil {
		if code := reservation.Code(err); code != "" {
			log.Fatal(code)
		}
		log.Fatal("quota_configuration_invalid")
	}
	defer cleanupReservation()
	usageSummary, cleanupUsageSummary, err := openUsageSummary(os.Getenv)
	if err != nil {
		log.Fatal("usage_kafka_summary_configuration_invalid")
	}
	defer cleanupUsageSummary()
	embeddingHandler, err := embedding.NewHandler(store, embeddingProviders)
	if err != nil {
		log.Fatal("embedding_handler_configuration_invalid")
	}
	recorder := observability.NewRecorder(observability.DefaultCapacity, nil, nil)
	chatGate := reservationGate
	var money *billing.Store
	if catalog := os.Getenv("AGENTMESH_BILLING_CATALOG"); catalog != "" {
		quota, ok := reservationGate.(*reservation.Runtime)
		if !ok {
			log.Fatal("billing_requires_persistent_storage")
		}
		money, err = billing.New(ctx, quota.DB, []byte(catalog))
		if err != nil || money.VerifyProviders(providers) != nil {
			log.Fatal("billing_catalog_invalid")
		}
		if err = money.Provision(ctx, os.Getenv("AGENTMESH_BOOTSTRAP_TENANT_ID")); err != nil {
			log.Fatal("billing_wallet_unavailable")
		}
		chatGate = money
		go func() {
			tick := time.NewTicker(15 * time.Second)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					step, cancel := context.WithTimeout(ctx, 5*time.Second)
					if money.Recover(step) != nil {
						log.Print("billing_recovery_failed")
					}
					cancel()
				}
			}
		}()
	}
	// Embedding trace records share the queryable recorder with chat.
	server := gateway.NewWithTenantRoutingAndRecorderAndReservations(resolver, recorder, chatGate)
	server.SetRateGate(rateRuntime.Gate)
	server.SetAdmissionLimits(128, 32, 2*time.Minute)
	embeddingHandler.SetResources(reservationGate, recorder)
	server.SetEmbeddingHandler(embeddingHandler)
	if os.Getenv("AGENTMESH_SEMANTIC_CACHE") == "true" {
		quota, ok := reservationGate.(*reservation.Runtime)
		if !ok || len(embeddingProviders) != 1 || os.Getenv("AGENTMESH_CACHE_VERSION") == "" || os.Getenv("AGENTMESH_EMBEDDING_MODEL") == "" {
			log.Fatal("cache_configuration_invalid")
		}
		versionRaw, _ := json.Marshal([]string{os.Getenv("AGENTMESH_CACHE_VERSION"), *providerOrder, os.Getenv("ARK_BASE_URL"), os.Getenv("ARK_MODEL"), os.Getenv("OLLAMA_BASE_URL"), os.Getenv("OLLAMA_MODEL"), os.Getenv("COMPATIBLE_BASE_URL"), os.Getenv("COMPATIBLE_MODELS")})
		h := sha256.Sum256(versionRaw)
		cache := &semanticcache.Cache{Redis: quota.Redis, Embedder: embeddingProviders[0], EmbeddingModel: os.Getenv("AGENTMESH_EMBEDDING_MODEL"), Version: hex.EncodeToString(h[:]), Threshold: .995, TTL: 10 * time.Minute}
		var hit func(context.Context, string, string) error
		if money != nil {
			hit = money.RecordCacheHit
		}
		server.SetSemanticCache(cache, func(ctx context.Context, t, m string) string {
			route, ok := store.Route(ctx, t, m)
			if !ok {
				return ""
			}
			raw, _ := json.Marshal(route)
			return string(raw)
		}, hit)
	}
	if quota, ok := reservationGate.(*reservation.Runtime); ok {
		go quota.RunRecovery(ctx, 15*time.Second, 5*time.Minute, func(error) { log.Print("quota_recovery_failed") })
	}
	log.Printf("AgentMesh gateway listening on http://%s", *address)
	protected := server.AuthenticatedHandler(func(next http.Handler) http.Handler {
		return auth.Authenticate(store, next)
	})
	root := http.NewServeMux()
	if money != nil {
		root.Handle("/v1/billing/", auth.Authenticate(store, money.Handler()))
		root.Handle("/admin/billing/", money.Admin(os.Getenv("AGENTMESH_ADMIN_TOKEN")))
	}
	root.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	root.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		check, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if rateRuntime.Health != nil && rateRuntime.Health(check) != nil {
			w.WriteHeader(503)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "unavailable"})
			return
		}
		if ctx.Err() != nil {
			w.WriteHeader(503)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "draining"})
			return
		}
		if money != nil && money.Health(check) != nil {
			w.WriteHeader(503)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "billing_unavailable"})
			return
		}
		if healthy, ok := store.(interface{ Health(context.Context) error }); ok {
			if err := healthy.Health(check); err != nil {
				w.WriteHeader(503)
				_ = json.NewEncoder(w).Encode(map[string]string{"status": "unavailable"})
				return
			}
		}
		if healthy, ok := reservationGate.(interface{ Health(context.Context) error }); ok {
			if err := healthy.Health(check); err != nil {
				w.WriteHeader(503)
				_ = json.NewEncoder(w).Encode(map[string]string{"status": "unavailable"})
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ready"})
	})
	if configuredStore.Lifecycle != nil {
		adminHandler := admin.NewHandler(configuredStore.Lifecycle, configuredStore.AdminTokenHash, func(route []string) bool {
			return tenant.RouteAllowed(route, logicalProviders)
		})
		if quota, ok := reservationGate.(*reservation.Runtime); ok {
			adminHandler.SetQuota(func(ctx context.Context, id string) error {
				if e := quota.Provision(ctx, id); e != nil {
					return e
				}
				if money != nil {
					return money.Provision(ctx, id)
				}
				return nil
			}, quota.Grant)
		}
		if usageSummary != nil {
			adminHandler.SetUsageSummary(func(ctx context.Context) (any, error) { return usageSummary.Summary(ctx) })
		}
		root.Handle("/admin/", adminHandler)
	}
	root.Handle("/", protected)
	metrics, err := telemetry.New(ctx, "agentmesh", os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"))
	if err != nil {
		log.Fatal("telemetry_configuration_invalid")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = metrics.Close(cleanup)
	}()
	root.Handle("/metrics", metrics.Metrics())
	if quota, ok := reservationGate.(*reservation.Runtime); ok {
		metrics.WatchSQL(ctx, quota.DB, "backend_active_reservations", "Reservations awaiting terminal state; -1 means store unavailable.", "SELECT COUNT(*) FROM quota_reservations WHERE state NOT IN ('settled','cancelled')")
	}
	if usageSummary != nil {
		metrics.WatchSQL(ctx, usageSummary.DB, "backend_usage_pending_outbox", "Unpublished usage events; -1 means store unavailable.", "SELECT COUNT(*) FROM usage_kafka_outbox WHERE published_at IS NULL")
	}
	if money != nil {
		metrics.WatchSQL(ctx, money.DB, "backend_billing_pending_reconciliation", "Money requests requiring upstream usage proof; -1 means store unavailable.", "SELECT COUNT(*) FROM billing_requests WHERE state='pending_reconciliation'")
		metrics.WatchSQL(ctx, money.DB, "backend_billing_expired_holds", "Money holds beyond the recovery threshold; -1 means store unavailable.", "SELECT COUNT(*) FROM billing_requests WHERE state='held' AND updated_at<DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 5 MINUTE)")
	}
	httpServer := newHTTPServer(*address, metrics.Wrap(root))
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdown); err != nil {
			_ = httpServer.Close()
		}
	}()
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal("http_server_failed")
	}
	<-shutdownDone
}

func openUsageSummary(lookup func(string) string) (*usagekafka.MySQLStore, func(), error) {
	if lookup == nil {
		return nil, func() {}, nil
	}
	dsn := lookup("AGENTMESH_USAGE_KAFKA_MYSQL_DSN")
	if dsn == "" {
		return nil, func() {}, nil
	}
	_, db, err := reservation.OpenMySQLRepository(dsn, nil)
	if err != nil {
		return nil, func() {}, err
	}
	return &usagekafka.MySQLStore{DB: db}, func() { _ = db.Close() }, nil
}

func validateServerAddress(address string, allowContainerListen bool) error {
	if err := gateway.ValidateListenAddress(address); err == nil {
		return nil
	}
	if !allowContainerListen {
		return fmt.Errorf("listen address must be 127.0.0.1:PORT")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil || host != "0.0.0.0" || port == "" {
		return fmt.Errorf("container listen address must be 0.0.0.0:PORT")
	}
	return nil
}
