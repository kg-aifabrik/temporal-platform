# Multi-stage build for a team worker image.
#
# Build one image per team by passing the TEAM build arg. Add --platform when the
# target cluster's arch differs from the build host (GKE nodes are amd64):
#   docker build --platform linux/amd64 --build-arg TEAM=compute-provisioning -t temporal-worker-compute-provisioning:dev workers/
#   docker build --platform linux/amd64 --build-arg TEAM=team-b -t temporal-worker-team-b:dev workers/
#
# The build stage runs on the host's native arch (BUILDPLATFORM) and cross-compiles
# to the target arch — with CGO off this is trivial and much faster than emulating
# the toolchain under QEMU.
#
# The local runbook runs workers as host processes for simplicity; this image is
# the in-cluster / production path (deploy as a Deployment per team).
ARG TEAM=compute-provisioning

FROM --platform=$BUILDPLATFORM golang:1.26 AS build
ARG TEAM
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /out/worker ./${TEAM}

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/worker /worker
ENTRYPOINT ["/worker"]
