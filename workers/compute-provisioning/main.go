// Compute-provisioning worker: a mock bare-metal → Kubernetes provisioning pipeline.
//
// The workflow orchestrates five activities that stand in for the real steps in
// the data-center flow (allocate hardware → install OS via Rafay → configure
// network → install Kubernetes → verify). Activities just log and sleep, except
// InstallOS, which fails on its first attempt so the retry shows up in the Web
// UI event history.
//
// Run:
//
//	TEMPORAL_NAMESPACE=compute-provisioning go run ./compute-provisioning
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/kg-aifabrik/temporal-platform/workers/internal/temporalclient"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// TaskQueue is the routing key between the worker and workflow starts.
const TaskQueue = "provisioning-tq"

// ProvisionRequest is the workflow input. JSON tags let it be passed straight
// from the `temporal workflow start --input` flag.
type ProvisionRequest struct {
	ClusterName string `json:"clusterName"`
	NodeCount   int    `json:"nodeCount"`
}

// StatusQuery is the query type a client (e.g. the gRPC frontend in
// examples/api-frontend) uses to read step-by-step progress.
const StatusQuery = "provisioningStatus"

// ProvisioningStatus is the queryable progress snapshot. A client decodes it into
// a struct with matching JSON tags — that shared shape is the whole contract.
type ProvisioningStatus struct {
	CurrentStep string   `json:"currentStep"`
	Messages    []string `json:"messages"`
	Done        bool     `json:"done"`
}

// ProvisionClusterWorkflow runs the pipeline end to end and returns a summary.
// It publishes progress through a query handler so callers can watch detailed
// status without waiting for the workflow to finish.
func ProvisionClusterWorkflow(ctx workflow.Context, req ProvisionRequest) (string, error) {
	// Progress state, exposed via query. Query handlers run on the workflow
	// goroutine (never concurrently), so reading this snapshot needs no locking.
	status := &ProvisioningStatus{CurrentStep: "starting"}
	step := func(s string) { status.CurrentStep = s; status.Messages = append(status.Messages, s) }
	note := func(m string) { status.Messages = append(status.Messages, "  "+m) }
	if err := workflow.SetQueryHandler(ctx, StatusQuery, func() (ProvisioningStatus, error) {
		return *status, nil
	}); err != nil {
		return "", err
	}

	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval: time.Second,
			MaximumAttempts: 5,
		},
	})
	log := workflow.GetLogger(ctx)
	log.Info("Provisioning started", "cluster", req.ClusterName, "nodes", req.NodeCount)

	step("allocating bare-metal assets")
	var allocated string
	if err := workflow.ExecuteActivity(ctx, AllocateBareMetal, req).Get(ctx, &allocated); err != nil {
		return "", err
	}
	note(allocated)

	// Install the OS on every node (InstallOS retries once — see the activity).
	step("installing OS on nodes")
	for i := 1; i <= req.NodeCount; i++ {
		node := fmt.Sprintf("%s-node-%d", req.ClusterName, i)
		var osResult string
		if err := workflow.ExecuteActivity(ctx, InstallOS, node).Get(ctx, &osResult); err != nil {
			return "", err
		}
		note(osResult)
	}

	// A short durable timer between phases makes the timeline readable in the UI.
	_ = workflow.Sleep(ctx, 2*time.Second)

	step("configuring network")
	var netResult, k8sResult, verifyResult string
	if err := workflow.ExecuteActivity(ctx, ConfigureNetwork, req.ClusterName).Get(ctx, &netResult); err != nil {
		return "", err
	}
	note(netResult)

	step("installing kubernetes")
	if err := workflow.ExecuteActivity(ctx, InstallKubernetes, req).Get(ctx, &k8sResult); err != nil {
		return "", err
	}
	note(k8sResult)

	step("verifying cluster")
	if err := workflow.ExecuteActivity(ctx, VerifyCluster, req.ClusterName).Get(ctx, &verifyResult); err != nil {
		return "", err
	}
	note(verifyResult)

	status.CurrentStep = "completed"
	status.Done = true
	summary := fmt.Sprintf("cluster %q provisioned on %d node(s): %s", req.ClusterName, req.NodeCount, verifyResult)
	log.Info("Provisioning complete", "summary", summary)
	return summary, nil
}

// --- Activities (each mocks real work with a log line and a short sleep) ---

func AllocateBareMetal(ctx context.Context, req ProvisionRequest) (string, error) {
	activity.GetLogger(ctx).Info("Allocating bare-metal assets", "cluster", req.ClusterName)
	time.Sleep(1 * time.Second)
	return fmt.Sprintf("%d server(s) reserved", req.NodeCount), nil
}

// InstallOS mocks a Rafay OS install that hits a transient failure the first
// time and succeeds on retry — so the Web UI shows an activity with attempt 2.
func InstallOS(ctx context.Context, node string) (string, error) {
	attempt := activity.GetInfo(ctx).Attempt
	activity.GetLogger(ctx).Info("Installing OS via Rafay", "node", node, "attempt", attempt)
	time.Sleep(1 * time.Second)
	if attempt == 1 {
		return "", fmt.Errorf("rafay transient error installing OS on %s (attempt %d)", node, attempt)
	}
	return "ubuntu-22.04 installed on " + node, nil
}

func ConfigureNetwork(ctx context.Context, cluster string) (string, error) {
	activity.GetLogger(ctx).Info("Configuring network (VLANs, CNI)", "cluster", cluster)
	time.Sleep(1 * time.Second)
	return "network configured", nil
}

func InstallKubernetes(ctx context.Context, req ProvisionRequest) (string, error) {
	activity.GetLogger(ctx).Info("Installing Kubernetes control plane + workers", "cluster", req.ClusterName)
	time.Sleep(2 * time.Second)
	return "kubernetes installed", nil
}

func VerifyCluster(ctx context.Context, cluster string) (string, error) {
	activity.GetLogger(ctx).Info("Verifying cluster readiness", "cluster", cluster)
	time.Sleep(1 * time.Second)
	return "all nodes Ready", nil
}

func main() {
	c, err := temporalclient.New()
	if err != nil {
		log.Fatalf("dial temporal: %v", err)
	}
	defer c.Close()

	w := worker.New(c, TaskQueue, worker.Options{})
	w.RegisterWorkflow(ProvisionClusterWorkflow)
	w.RegisterActivity(AllocateBareMetal)
	w.RegisterActivity(InstallOS)
	w.RegisterActivity(ConfigureNetwork)
	w.RegisterActivity(InstallKubernetes)
	w.RegisterActivity(VerifyCluster)

	log.Printf("compute-provisioning worker listening on task queue %q", TaskQueue)
	if err := w.Run(worker.InterruptCh()); err != nil {
		log.Fatalf("worker: %v", err)
	}
}
