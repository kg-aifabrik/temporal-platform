# Multi-stage build for a team worker image.
#
# Build one image per team by passing the TEAM build arg:
#   docker build --build-arg TEAM=compute-provisioning -t temporal-worker-compute-provisioning:dev workers/
#   docker build --build-arg TEAM=team-b -t temporal-worker-team-b:dev workers/
#
# The local runbook runs workers as host processes for simplicity; this image is
# the in-cluster / production path (deploy as a Deployment per team).
ARG TEAM=compute-provisioning

FROM golang:1.26 AS build
ARG TEAM
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/worker ./${TEAM}

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/worker /worker
ENTRYPOINT ["/worker"]
