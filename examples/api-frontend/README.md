# api-frontend

A Go gRPC + Proto frontend for the `compute-provisioning` `ProvisionClusterWorkflow`:
submit a provisioning request, and get its status with detailed step messages.

It's a worked example of the Temporal entry-point pattern — a thin gRPC service
whose handlers are Temporal client calls (`ExecuteWorkflow`, `DescribeWorkflowExecution`,
`QueryWorkflow`). Copy it as a starting point for your own frontend.

Full how-to guide, build/deploy steps, and tested results:
[`docs/api-frontend-for-temporal.md`](../../docs/api-frontend-for-temporal.md).

```bash
go test ./...                              # unit tests
buf generate                               # regenerate stubs after editing the .proto
go run ./cmd/server                        # run the gRPC server
go run ./cmd/apiclient -cluster edge-42    # submit + poll a request
```
