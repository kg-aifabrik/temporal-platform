// Package service implements the ProvisioningService gRPC server by translating
// each RPC into Temporal client calls against the compute-provisioning backend.
//
// The mapping is deliberately thin:
//   - SubmitProvisioningRequest  -> client.ExecuteWorkflow(ProvisionClusterWorkflow)
//   - GetProvisioningStatus      -> client.DescribeWorkflowExecution + QueryWorkflow
//
// The service holds only a Temporal client and knows the backend by contract —
// the workflow type name, task queue, and the shapes of the input and the status
// query. It does not import the worker package, so a team can build their own
// frontend for any workflow the same way.
package service

import (
	"context"

	provisioningv1 "github.com/kg-aifabrik/temporal-platform/examples/api-frontend/gen/provisioning/v1"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Backend contract — must match the compute-provisioning worker.
const (
	// WorkflowType is the registered workflow function name.
	WorkflowType = "ProvisionClusterWorkflow"
	// TaskQueue routes workflow starts to the compute-provisioning workers.
	TaskQueue = "provisioning-tq"
	// StatusQuery is the query handler the workflow registers for step detail.
	StatusQuery = "provisioningStatus"
)

// provisionRequest mirrors the workflow input. Field names/JSON tags must match
// the worker's struct so the default (JSON) data converter round-trips it.
type provisionRequest struct {
	ClusterName string `json:"clusterName"`
	NodeCount   int    `json:"nodeCount"`
}

// provisioningStatus mirrors the workflow's query return value.
type provisioningStatus struct {
	CurrentStep string   `json:"currentStep"`
	Messages    []string `json:"messages"`
	Done        bool     `json:"done"`
}

// Service implements provisioningv1.ProvisioningServiceServer.
type Service struct {
	provisioningv1.UnimplementedProvisioningServiceServer
	client client.Client
}

// New returns a Service backed by the given Temporal client.
func New(c client.Client) *Service { return &Service{client: c} }

// SubmitProvisioningRequest starts (or idempotently re-attaches to) the workflow.
func (s *Service) SubmitProvisioningRequest(ctx context.Context, req *provisioningv1.SubmitProvisioningRequestRequest) (*provisioningv1.SubmitProvisioningRequestResponse, error) {
	if req.GetClusterName() == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_name is required")
	}
	if req.GetNodeCount() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "node_count must be > 0")
	}

	// The workflow id is the idempotency key. USE_EXISTING means a duplicate
	// submit while a run is in flight re-attaches to it rather than erroring.
	workflowID := req.GetRequestId()
	if workflowID == "" {
		workflowID = "provision-" + req.GetClusterName()
	}

	run, err := s.client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                       workflowID,
		TaskQueue:                TaskQueue,
		WorkflowIDConflictPolicy: enumspb.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
	}, WorkflowType, provisionRequest{
		ClusterName: req.GetClusterName(),
		NodeCount:   int(req.GetNodeCount()),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "start workflow: %v", err)
	}

	return &provisioningv1.SubmitProvisioningRequestResponse{
		RequestId: run.GetID(),
		RunId:     run.GetRunID(),
	}, nil
}

// GetProvisioningStatus reports execution state plus the workflow's step detail.
func (s *Service) GetProvisioningStatus(ctx context.Context, req *provisioningv1.GetProvisioningStatusRequest) (*provisioningv1.GetProvisioningStatusResponse, error) {
	if req.GetRequestId() == "" {
		return nil, status.Error(codes.InvalidArgument, "request_id is required")
	}

	desc, err := s.client.DescribeWorkflowExecution(ctx, req.GetRequestId(), "")
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "no such request %q: %v", req.GetRequestId(), err)
	}
	info := desc.GetWorkflowExecutionInfo()
	runID := info.GetExecution().GetRunId()

	resp := &provisioningv1.GetProvisioningStatusResponse{
		RequestId: req.GetRequestId(),
		RunId:     runID,
		State:     mapState(info.GetStatus()),
	}

	// Detailed step messages come from a workflow query. Queries work while the
	// workflow is running and on closed workflows still within retention (the
	// server replays history). A query failure is non-fatal — we still return
	// the execution state from Describe.
	if val, qErr := s.client.QueryWorkflow(ctx, req.GetRequestId(), runID, StatusQuery); qErr == nil && val.HasValue() {
		var st provisioningStatus
		if val.Get(&st) == nil {
			resp.CurrentStep = st.CurrentStep
			resp.Messages = st.Messages
		}
	}

	// On a terminal state, surface the workflow's result or error. GetWorkflow().
	// Get() returns immediately for a closed workflow (it does not block here).
	switch info.GetStatus() {
	case enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED:
		var result string
		if err := s.client.GetWorkflow(ctx, req.GetRequestId(), runID).Get(ctx, &result); err == nil {
			resp.Result = result
		}
	case enumspb.WORKFLOW_EXECUTION_STATUS_FAILED:
		if err := s.client.GetWorkflow(ctx, req.GetRequestId(), runID).Get(ctx, nil); err != nil {
			resp.ErrorMessage = err.Error()
		}
	}

	return resp, nil
}

// mapState translates a Temporal execution status into the proto State enum.
func mapState(s enumspb.WorkflowExecutionStatus) provisioningv1.State {
	switch s {
	case enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING:
		return provisioningv1.State_STATE_RUNNING
	case enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED:
		return provisioningv1.State_STATE_COMPLETED
	case enumspb.WORKFLOW_EXECUTION_STATUS_FAILED:
		return provisioningv1.State_STATE_FAILED
	case enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED:
		return provisioningv1.State_STATE_CANCELED
	case enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED:
		return provisioningv1.State_STATE_TERMINATED
	case enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT:
		return provisioningv1.State_STATE_TIMED_OUT
	default:
		return provisioningv1.State_STATE_UNSPECIFIED
	}
}
