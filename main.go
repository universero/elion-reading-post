package main

import (
	"gitlab.aiecnu.net/elion/elion-reading-post/infra/config"
	"gitlab.aiecnu.net/elion/elion-reading-post/post"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/contrib/propagators/b3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"net/http"
)

func Init() {
	// 设置openTelemetry的传播器，用于分布式追踪中传递上下文信息
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(b3.New(), propagation.Baggage{}, propagation.TraceContext{}))
	http.DefaultTransport = otelhttp.NewTransport(http.DefaultTransport)
}

func main() {
	Init()
	post.NewManager(config.GetConfig().Consumers).Run()
}
