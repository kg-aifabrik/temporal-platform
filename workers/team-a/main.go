// Team A worker: a mock bare-metal → Kubernetes provisioning pipeline.
//
// The workflow orchestrates five activities that stand in for the real steps in
// the data-center flow (allocate hardware → install OS via Rafay → configure
// network → install Kubernetes → verify). Activities just log and sleep, except
// InstallOS, which fails on its first attempt so the retry shows up in the Web
// UI event history.
//
// Run:
//   TEMPORAL_NAMESPACE=team-a go run ./team-a
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

// ProvisionClusterWorkflow runs the pipeline end to end and returns a summary.
func ProvisionClusterWorkflow(ctx workflow.Context, req ProvisionRequest) (string, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval: time.Second,
			MaximumAttempts: 5,
		},
	})
	log := workflow.GetLogger(ctx)
	log.Info("Provisioning started", "cluster", req.ClusterName, "nodes", req.NodeCount)

	var allocated string
	if err := workflow.ExecuteActivity(ctx, AllocateBareMetal, req).Get(ctx, &allocated); err != nil {
		return "", err
	}

	// Install the OS on every node (InstallOS retries once — see the activity).
	for i := 1; i <= req.NodeCount; i++ {
		node := fmt.Sprintf("%s-node-%d", req.ClusterName, i)
		var osResult string
		if err := workflow.ExecuteActivity(ctx, InstallOS, node).Get(ctx, &osResult); err != nil {
			return "", err
		}
	}

	// A short durable timer between phases makes the timeline readable in the UI.
	_ = workflow.Sleep(ctx, 2*time.Second)

	var netResult, k8sResult, verifyResult string
	if err := workflow.ExecuteActivity(ctx, ConfigureNetwork, req.ClusterName).Get(ctx, &netResult); err != nil {
		return "", err
	}
	if err := workflow.ExecuteActivity(ctx, InstallKubernetes, req).Get(ctx, &k8sResult); err != nil {
		return "", err
	}
	if err := workflow.ExecuteActivity(ctx, VerifyCluster, req.ClusterName).Get(ctx, &verifyResult); err != nil {
		return "", err
	}

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

	log.Printf("team-a worker listening on task queue %q", TaskQueue)
	if err := w.Run(worker.InterruptCh()); err != nil {
		log.Fatalf("worker: %v", err)
	}
}
