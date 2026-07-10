// Command apiclient is a tiny gRPC client for exercising the ProvisioningService
// end to end: it submits a provisioning request, then polls status until the
// workflow reaches a terminal state, printing step messages as they appear.
//
//	go run ./cmd/apiclient -addr localhost:9233 -cluster edge-42 -nodes 3
package main

import (
	"context"
	"flag"
	"log"
	"time"

	provisioningv1 "github.com/kg-aifabrik/temporal-platform/examples/api-frontend/gen/provisioning/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	addr := flag.String("addr", "localhost:9233", "ProvisioningService gRPC address")
	cluster := flag.String("cluster", "edge-demo", "cluster name to provision")
	nodes := flag.Int("nodes", 3, "node count")
	requestID := flag.String("request-id", "", "idempotency key / workflow id (default derived from cluster)")
	timeout := flag.Duration("timeout", 2*time.Minute, "overall timeout")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	conn, err := grpc.NewClient(*addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("connect %s: %v", *addr, err)
	}
	defer conn.Close()
	api := provisioningv1.NewProvisioningServiceClient(conn)

	sub, err := api.SubmitProvisioningRequest(ctx, &provisioningv1.SubmitProvisioningRequestRequest{
		RequestId:   *requestID,
		ClusterName: *cluster,
		NodeCount:   int32(*nodes),
	})
	if err != nil {
		log.Fatalf("submit: %v", err)
	}
	log.Printf("submitted: request_id=%s run_id=%s", sub.GetRequestId(), sub.GetRunId())

	seen := 0
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		st, err := api.GetProvisioningStatus(ctx, &provisioningv1.GetProvisioningStatusRequest{RequestId: sub.GetRequestId()})
		if err != nil {
			log.Fatalf("status: %v", err)
		}
		// print any new step messages since the last poll
		for _, m := range st.GetMessages()[min(seen, len(st.GetMessages())):] {
			log.Printf("  · %s", m)
		}
		seen = len(st.GetMessages())

		if s := st.GetState(); s != provisioningv1.State_STATE_RUNNING && s != provisioningv1.State_STATE_UNSPECIFIED {
			log.Printf("state=%s current_step=%q", s, st.GetCurrentStep())
			if r := st.GetResult(); r != "" {
				log.Printf("result: %s", r)
			}
			if e := st.GetErrorMessage(); e != "" {
				log.Printf("error: %s", e)
			}
			return
		}
		select {
		case <-ctx.Done():
			log.Fatalf("timed out waiting for completion (last step %q)", st.GetCurrentStep())
		case <-ticker.C:
		}
	}
}
