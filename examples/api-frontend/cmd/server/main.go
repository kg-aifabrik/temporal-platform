// Command server runs the ProvisioningService gRPC frontend.
//
// It dials the Temporal frontend (as a client — no database access) and serves
// the gRPC API. Configuration is by environment, matching the workers:
//
//	TEMPORAL_ADDRESS    frontend host:port (default localhost:7233)
//	TEMPORAL_NAMESPACE  namespace to start workflows in (default compute-provisioning)
//	GRPC_PORT           port to listen on (default 9233)
//
// gRPC server reflection is enabled so grpcurl and the bundled apiclient can call
// it without a compiled stub, and a standard gRPC health service is registered.
package main

import (
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	provisioningv1 "github.com/kg-aifabrik/temporal-platform/examples/api-frontend/gen/provisioning/v1"
	"github.com/kg-aifabrik/temporal-platform/examples/api-frontend/internal/service"
	"go.temporal.io/sdk/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	addr := env("TEMPORAL_ADDRESS", "localhost:7233")
	namespace := env("TEMPORAL_NAMESPACE", "compute-provisioning")
	port := env("GRPC_PORT", "9233")

	c, err := client.Dial(client.Options{HostPort: addr, Namespace: namespace})
	if err != nil {
		log.Fatalf("dial temporal at %s: %v", addr, err)
	}
	defer c.Close()

	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("listen :%s: %v", port, err)
	}

	grpcServer := grpc.NewServer()
	provisioningv1.RegisterProvisioningServiceServer(grpcServer, service.New(c))

	hs := health.NewServer()
	hs.SetServingStatus("provisioning.v1.ProvisioningService", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(grpcServer, hs)
	reflection.Register(grpcServer)

	// Graceful shutdown on SIGINT/SIGTERM so in-flight RPCs drain.
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Println("shutting down")
		grpcServer.GracefulStop()
	}()

	log.Printf("ProvisioningService listening on :%s (temporal %s, namespace %s)", port, addr, namespace)
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
