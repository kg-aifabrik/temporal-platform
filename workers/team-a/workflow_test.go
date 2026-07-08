// Unit test for the provisioning workflow, using the Temporal Go SDK's
// time-skipping test environment. Durable timers (workflow.Sleep) are skipped
// instantly and the activities are mocked, so this runs in milliseconds without
// a running Temporal server. This is the fast feedback loop referenced in
// docs/writing-workflows.md.
package main

import (
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
)

func TestProvisionClusterWorkflow(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	// Mock every activity so the test exercises the workflow's orchestration,
	// not the (sleep-heavy) activity bodies.
	env.OnActivity(AllocateBareMetal, mock.Anything, mock.Anything).Return("2 servers reserved", nil)
	env.OnActivity(InstallOS, mock.Anything, mock.Anything).Return("ubuntu-22.04 installed", nil)
	env.OnActivity(ConfigureNetwork, mock.Anything, mock.Anything).Return("network configured", nil)
	env.OnActivity(InstallKubernetes, mock.Anything, mock.Anything).Return("kubernetes installed", nil)
	env.OnActivity(VerifyCluster, mock.Anything, mock.Anything).Return("all nodes Ready", nil)

	env.ExecuteWorkflow(ProvisionClusterWorkflow, ProvisionRequest{ClusterName: "edge-01", NodeCount: 2})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var result string
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Contains(t, result, `cluster "edge-01" provisioned on 2 node(s)`)
}
