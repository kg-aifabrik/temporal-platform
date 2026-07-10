package service

import (
	"context"
	"encoding/json"
	"testing"

	provisioningv1 "github.com/kg-aifabrik/temporal-platform/examples/api-frontend/gen/provisioning/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/mocks"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeEncodedValue stands in for the converter.EncodedValue returned by
// QueryWorkflow, decoding via JSON — enough to exercise status mapping.
type fakeEncodedValue struct{ v any }

func (f fakeEncodedValue) HasValue() bool { return f.v != nil }
func (f fakeEncodedValue) Get(ptr any) error {
	b, err := json.Marshal(f.v)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, ptr)
}

func describeResp(wfID, runID string, st enumspb.WorkflowExecutionStatus) *workflowservice.DescribeWorkflowExecutionResponse {
	return &workflowservice.DescribeWorkflowExecutionResponse{
		WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
			Execution: &commonpb.WorkflowExecution{WorkflowId: wfID, RunId: runID},
			Status:    st,
		},
	}
}

// SubmitProvisioningRequest starts the workflow on the right task queue, uses the
// request_id as the workflow id, and returns the run identifiers.
func TestSubmit_Success(t *testing.T) {
	mc := &mocks.Client{}
	run := &mocks.WorkflowRun{}
	run.On("GetID").Return("req-abc")
	run.On("GetRunID").Return("run-1")

	mc.On("ExecuteWorkflow", mock.Anything,
		mock.MatchedBy(func(o client.StartWorkflowOptions) bool {
			return o.ID == "req-abc" && o.TaskQueue == TaskQueue &&
				o.WorkflowIDConflictPolicy == enumspb.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING
		}),
		WorkflowType, mock.Anything,
	).Return(run, nil)

	svc := New(mc)
	resp, err := svc.SubmitProvisioningRequest(context.Background(), &provisioningv1.SubmitProvisioningRequestRequest{
		RequestId:   "req-abc",
		ClusterName: "edge-1",
		NodeCount:   3,
	})

	require.NoError(t, err)
	assert.Equal(t, "req-abc", resp.GetRequestId())
	assert.Equal(t, "run-1", resp.GetRunId())
	mc.AssertExpectations(t)
}

// With no request_id, the workflow id is derived from the cluster name.
func TestSubmit_DerivesWorkflowID(t *testing.T) {
	mc := &mocks.Client{}
	run := &mocks.WorkflowRun{}
	run.On("GetID").Return("provision-edge-9")
	run.On("GetRunID").Return("run-9")
	mc.On("ExecuteWorkflow", mock.Anything,
		mock.MatchedBy(func(o client.StartWorkflowOptions) bool { return o.ID == "provision-edge-9" }),
		WorkflowType, mock.Anything,
	).Return(run, nil)

	svc := New(mc)
	_, err := svc.SubmitProvisioningRequest(context.Background(), &provisioningv1.SubmitProvisioningRequestRequest{
		ClusterName: "edge-9", NodeCount: 1,
	})
	require.NoError(t, err)
	mc.AssertExpectations(t)
}

// Missing/invalid fields are rejected with InvalidArgument before touching Temporal.
func TestSubmit_Validation(t *testing.T) {
	svc := New(&mocks.Client{})
	for _, tc := range []struct {
		name string
		req  *provisioningv1.SubmitProvisioningRequestRequest
	}{
		{"no cluster name", &provisioningv1.SubmitProvisioningRequestRequest{NodeCount: 1}},
		{"zero nodes", &provisioningv1.SubmitProvisioningRequestRequest{ClusterName: "edge-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.SubmitProvisioningRequest(context.Background(), tc.req)
			assert.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
}

// A running workflow returns RUNNING plus the queried step detail, and no result.
func TestGetStatus_Running(t *testing.T) {
	mc := &mocks.Client{}
	mc.On("DescribeWorkflowExecution", mock.Anything, "req-abc", "").
		Return(describeResp("req-abc", "run-1", enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING), nil)
	mc.On("QueryWorkflow", mock.Anything, "req-abc", "run-1", StatusQuery).
		Return(fakeEncodedValue{v: provisioningStatus{
			CurrentStep: "installing OS",
			Messages:    []string{"reserved 3 servers", "installing OS on edge-1-node-1"},
		}}, nil)

	svc := New(mc)
	resp, err := svc.GetProvisioningStatus(context.Background(), &provisioningv1.GetProvisioningStatusRequest{RequestId: "req-abc"})

	require.NoError(t, err)
	assert.Equal(t, provisioningv1.State_STATE_RUNNING, resp.GetState())
	assert.Equal(t, "installing OS", resp.GetCurrentStep())
	assert.Len(t, resp.GetMessages(), 2)
	assert.Empty(t, resp.GetResult())
	mc.AssertExpectations(t)
}

// A completed workflow returns COMPLETED and the workflow's result summary.
func TestGetStatus_Completed(t *testing.T) {
	mc := &mocks.Client{}
	mc.On("DescribeWorkflowExecution", mock.Anything, "req-abc", "").
		Return(describeResp("req-abc", "run-1", enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED), nil)
	mc.On("QueryWorkflow", mock.Anything, "req-abc", "run-1", StatusQuery).
		Return(fakeEncodedValue{v: provisioningStatus{CurrentStep: "done", Done: true}}, nil)

	run := &mocks.WorkflowRun{}
	run.On("Get", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) { *(args.Get(1).(*string)) = "cluster \"edge-1\" provisioned on 3 node(s)" }).
		Return(nil)
	mc.On("GetWorkflow", mock.Anything, "req-abc", "run-1").Return(run)

	svc := New(mc)
	resp, err := svc.GetProvisioningStatus(context.Background(), &provisioningv1.GetProvisioningStatusRequest{RequestId: "req-abc"})

	require.NoError(t, err)
	assert.Equal(t, provisioningv1.State_STATE_COMPLETED, resp.GetState())
	assert.Contains(t, resp.GetResult(), "provisioned on 3 node(s)")
	mc.AssertExpectations(t)
}

// An unknown request_id surfaces as NotFound.
func TestGetStatus_NotFound(t *testing.T) {
	mc := &mocks.Client{}
	mc.On("DescribeWorkflowExecution", mock.Anything, "missing", "").
		Return((*workflowservice.DescribeWorkflowExecutionResponse)(nil), assert.AnError)

	svc := New(mc)
	_, err := svc.GetProvisioningStatus(context.Background(), &provisioningv1.GetProvisioningStatusRequest{RequestId: "missing"})
	assert.Equal(t, codes.NotFound, status.Code(err))
}
