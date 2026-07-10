# Multi-stage build for the ProvisioningService gRPC frontend.
#
# Add --platform when the target cluster's arch differs from the build host
# (GKE nodes are amd64):
#   docker build --platform linux/amd64 -t temporal-api-frontend:dev examples/api-frontend/
#
# The build stage runs on the host's native arch and cross-compiles to the target
# (CGO off), which is fast and needs no emulation.
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /out/server ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/server /server
EXPOSE 9233
ENTRYPOINT ["/server"]
