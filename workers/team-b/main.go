// Team B worker: a small 5-step "order processing" toy workflow.
//
// It exists to show a second team, in its own namespace, with its own task
// queue and workflow type — independent of the compute-provisioning team. Five sequential activities,
// each a log line and a short sleep.
//
// Run:
//   TEMPORAL_NAMESPACE=team-b go run ./team-b
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

const TaskQueue = "orders-tq"

// OrderRequest is the workflow input (JSON-friendly for `--input`).
type OrderRequest struct {
	OrderID string  `json:"orderId"`
	Amount  float64 `json:"amount"`
}

// OrderWorkflow runs the five steps in order and returns a summary.
func OrderWorkflow(ctx workflow.Context, req OrderRequest) (string, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	})
	log := workflow.GetLogger(ctx)
	log.Info("Order received", "order", req.OrderID, "amount", req.Amount)

	steps := []any{ValidateOrder, ReserveInventory, ChargePayment, PackageShipment, SendNotification}
	for _, step := range steps {
		var out string
		if err := workflow.ExecuteActivity(ctx, step, req).Get(ctx, &out); err != nil {
			return "", err
		}
	}

	summary := fmt.Sprintf("order %s completed ($%.2f)", req.OrderID, req.Amount)
	log.Info("Order complete", "summary", summary)
	return summary, nil
}

// --- Activities ---

func ValidateOrder(ctx context.Context, req OrderRequest) (string, error) {
	activity.GetLogger(ctx).Info("Validating order", "order", req.OrderID)
	time.Sleep(500 * time.Millisecond)
	return "valid", nil
}

func ReserveInventory(ctx context.Context, req OrderRequest) (string, error) {
	activity.GetLogger(ctx).Info("Reserving inventory", "order", req.OrderID)
	time.Sleep(800 * time.Millisecond)
	return "reserved", nil
}

func ChargePayment(ctx context.Context, req OrderRequest) (string, error) {
	activity.GetLogger(ctx).Info("Charging payment", "order", req.OrderID, "amount", req.Amount)
	time.Sleep(1 * time.Second)
	return "charged", nil
}

func PackageShipment(ctx context.Context, req OrderRequest) (string, error) {
	activity.GetLogger(ctx).Info("Packaging shipment", "order", req.OrderID)
	time.Sleep(700 * time.Millisecond)
	return "packaged", nil
}

func SendNotification(ctx context.Context, req OrderRequest) (string, error) {
	activity.GetLogger(ctx).Info("Sending confirmation", "order", req.OrderID)
	time.Sleep(300 * time.Millisecond)
	return "notified", nil
}

func main() {
	c, err := temporalclient.New()
	if err != nil {
		log.Fatalf("dial temporal: %v", err)
	}
	defer c.Close()

	w := worker.New(c, TaskQueue, worker.Options{})
	w.RegisterWorkflow(OrderWorkflow)
	w.RegisterActivity(ValidateOrder)
	w.RegisterActivity(ReserveInventory)
	w.RegisterActivity(ChargePayment)
	w.RegisterActivity(PackageShipment)
	w.RegisterActivity(SendNotification)

	log.Printf("team-b worker listening on task queue %q", TaskQueue)
	if err := w.Run(worker.InterruptCh()); err != nil {
		log.Fatalf("worker: %v", err)
	}
}
