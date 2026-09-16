package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"agentmesh/internal/admin"         //后台管理接口(管理员路由,用量查询)
	"agentmesh/internal/auth"          //鉴权,密钥存储,身份校验中间件
	"agentmesh/internal/embedding"     //向量嵌入模型服务(文本的向量化接口)
	"agentmesh/internal/gateway"       //核心的AI网关,租户路由分发,请求流转核心
	"agentmesh/internal/observability" //可观测性：埋点、指标记录、请求统计
	"agentmesh/internal/ratelimit"     //全局限流运行时、流量控制
	"agentmesh/internal/reservation"   //资源配额 / 预留系统，限制租户调用额度
	"agentmesh/internal/runtime"       //AI 后端提供器管理（mock/ark/ollama 大模型）
	"agentmesh/internal/tenant"        //多租户路由解析，租户隔离逻辑
	"agentmesh/internal/usagekafka"    //调用用量统计，Kafka 上报 + MySQL 持久化
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
		// 写超时（WriteTimeout）保持置零，避免合法的服务器发送事件（SSE）长流被固定的绝对时钟超时强制断开。
		//数据流的终止边界仅由服务提供方设定的截止时间与客户端主动取消操作决定
		//防止多重重新编写重试的参数
		WriteTimeout: 0,
	}
}

func main() {
	flags := flag.NewFlagSet("api", flag.ExitOnError)
	address := flags.String("addr", "127.0.0.1:18080", "local listen address (must be 127.0.0.1:PORT)")
	allowContainerListen := flags.Bool("allow-container-listen", false, "allow 0.0.0.0:PORT only for container-local binding") //容器开启后可以提供外部访问
	providerOrder := flags.String("providers", "mock", "provider route: mock or comma-separated ark,ollama")
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
	resolver, err := tenant.NewResolver(startupContext, store, logicalProviders, providers)
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
	server := gateway.NewWithTenantRoutingAndRecorderAndReservations(resolver, observability.NewRecorder(observability.DefaultCapacity, nil, nil), reservationGate)
	server.SetRateGate(rateRuntime.Gate)
	embeddingHandler, err := embedding.NewHandler(store, embeddingProviders)
	if err != nil {
		log.Fatal("embedding_handler_configuration_invalid")
	}
	server.SetEmbeddingHandler(embeddingHandler)
	log.Printf("AgentMesh gateway listening on http://%s", *address)
	protected := server.AuthenticatedHandler(func(next http.Handler) http.Handler {
		return auth.Authenticate(store, next)
	})
	root := http.NewServeMux()
	root.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}\n`))
	})
	root.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ready"}\n`))
	})
	if configuredStore.Lifecycle != nil {
		adminHandler := admin.NewHandler(configuredStore.Lifecycle, configuredStore.AdminTokenHash, func(route []string) bool {
			return tenant.RouteAllowed(route, logicalProviders)
		})
		if usageSummary != nil {
			adminHandler.SetUsageSummary(func(ctx context.Context) (any, error) { return usageSummary.Summary(ctx) })
		}
		root.Handle("/admin/", adminHandler)
	}
	root.Handle("/", protected)
	if err := newHTTPServer(*address, root).ListenAndServe(); err != nil {
		log.Print(err)
	}
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
