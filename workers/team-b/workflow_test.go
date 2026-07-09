// Unit test for the order workflow, using the SDK's time-skipping test
// environment with mocked activities. Mirrors compute-provisioning's test — the template
// every team follows (see docs/test-plan.md).
package main

import (
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
)

func TestOrderWorkflow(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	for _, act := range []any{ValidateOrder, ReserveInventory, ChargePayment, PackageShipment, SendNotification} {
		env.OnActivity(act, mock.Anything, mock.Anything).Return("ok", nil)
	}

	env.ExecuteWorkflow(OrderWorkflow, OrderRequest{OrderID: "ORD-1", Amount: 42.50})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var result string
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Contains(t, result, "order ORD-1 completed")
}
