# Production on GCP (GKE + Cloud SQL)

The production deployment uses the **same Helm chart and topology** as the local
setup. Only the surrounding infrastructure and a few values change.

**Verified path:** [`runbooks/runbook-gke-cloud-sql.md`](../../runbooks/runbook-gke-cloud-sql.md)
is the step-by-step procedure (validated on `dev-fop` against Cloud SQL with IAM
database auth), and [`gke-values.yaml`](gke-values.yaml) is the real values file
it installs. This README is the *why* behind those — what changes from local and
the rationale. The auth/OIDC section below is a forward-looking sketch (RBAC was
off in the infra verify).

## What changes from local

| Concern | Local (Rancher) | Production (GCP) |
|---|---|---|
| Database | in-cluster PostgreSQL StatefulSet | **Cloud SQL for PostgreSQL**, regional HA (synchronous). Not in the cluster. |
| DB credentials | k8s Secret with a dev password | **IAM database auth** — no stored password. Server pods carry a Cloud SQL Auth Proxy native sidecar (`--auto-iam-authn`) that mints short-lived tokens from the pod's Workload Identity |
| DB connectivity | `postgres:5432` service | Cloud SQL Auth Proxy sidecar on `127.0.0.1:5432`, reaching the instance's **private IP** over the VPC (Private Service Access) |
| Sizing | 1 replica/service | 2+ replicas/service, PodDisruptionBudgets, resource requests/limits |
| Visibility | PostgreSQL advanced visibility | Same (PostgreSQL); add Elasticsearch/OpenSearch via Dual Visibility only if query load demands it |
| JWKS source | in-cluster nginx | the real identity provider's JWKS endpoint (Google, Keycloak, Okta) |
| Tokens | `auth/tokengen` demo tokens | issued by the identity provider; map groups → `namespace:role` claims |
| Web UI auth | pointed at internal-frontend (bypasses auth) | **OIDC login**, talks to the authorized external frontend |
| TLS | off (localhost) | mutual TLS on the frontend; TLS to Cloud SQL |
| Ingress | `kubectl port-forward` | internal load balancer / gRPC-capable ingress for the frontend; ingress for the UI |
| Metrics/dashboards | bundle kube-prometheus-stack | use the cluster's existing Prometheus + Grafana; apply only the ServiceMonitors + dashboards ([observability.md](../../docs/observability.md)) |

## Persistence (how the DB connection works)

See [`gke-values.yaml`](gke-values.yaml) for the applied version. Each server pod
runs a Cloud SQL Auth Proxy **native sidecar** that authenticates to Cloud SQL as
the pod's Workload Identity and exposes PostgreSQL on `127.0.0.1:5432`. Both
datastores connect there as the IAM user with **no password** — the proxy injects
a short-lived token:

```yaml
server:
  additionalInitContainers:
    - name: cloud-sql-proxy
      image: gcr.io/cloud-sql-connectors/cloud-sql-proxy:2.14.1
      restartPolicy: Always     # native sidecar
      args: ["--private-ip","--auto-iam-authn","--address=127.0.0.1","--port=5432","<project>:<region>:<instance>"]
  config:
    persistence:
      numHistoryShards: 512      # immutable — decide once
      datastores:
        default:
          sql:
            pluginName: postgres12
            databaseName: temporal
            connectAddr: "127.0.0.1:5432"   # the local proxy
            connectProtocol: "tcp"
            user: temporal-sql@<project>.iam # IAM DB user, no password
```

An alternative to the proxy sidecar is the server v1.31 `passwordCommand`, which
shells out to mint a Cloud SQL IAM token per connection; the proxy is used here
because it needs no helper binary in the image and handles token refresh itself.

## Auth values sketch

Same authorizer, real JWKS, and the UI on OIDC (drop the internal-frontend
shortcut used locally):

```yaml
server:
  config:
    authorization:
      jwtKeyProvider:
        keySourceURIs:
          - https://<your-idp>/.well-known/jwks.json
      permissionsClaimName: permissions
      authorizer: default
      claimMapper: default
web:
  additionalEnv:
    - name: TEMPORAL_AUTH_ENABLED
      value: "true"
    # TEMPORAL_AUTH_PROVIDER_URL / CLIENT_ID / CLIENT_SECRET / CALLBACK_URL ...
```

## Rationale

The design decisions — one cluster with per-team namespaces, PostgreSQL vs
Elasticsearch, the shard count, the auth model, and the multi-team gaps a
platform team must build — are documented in the research repo under
`research/temporal` (`shared-instance-architecture.md`,
`multi-tenancy-setup.md`).
