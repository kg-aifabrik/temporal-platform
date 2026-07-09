# Production on GCP (GKE + Cloud SQL)

The production deployment uses the **same Helm chart and topology** as the local
setup. Only the surrounding infrastructure and a few values change. This folder
is notes + a values skeleton, not an applied environment.

## What changes from local

| Concern | Local (Rancher) | Production (GCP) |
|---|---|---|
| Database | in-cluster PostgreSQL StatefulSet | **Cloud SQL for PostgreSQL**, regional HA (synchronous). Not in the cluster. |
| DB credentials | k8s Secret with a dev password | **`passwordCommand`** (server v1.31) minting short-lived Cloud SQL IAM tokens — no stored password |
| DB connectivity | `postgres:5432` service | Private IP over VPC, or Cloud SQL Auth Proxy sidecar |
| Sizing | 1 replica/service | 2+ replicas/service, PodDisruptionBudgets, resource requests/limits |
| Visibility | PostgreSQL advanced visibility | Same (PostgreSQL); add Elasticsearch/OpenSearch via Dual Visibility only if query load demands it |
| JWKS source | in-cluster nginx | the real identity provider's JWKS endpoint (Google, Keycloak, Okta) |
| Tokens | `auth/tokengen` demo tokens | issued by the identity provider; map groups → `namespace:role` claims |
| Web UI auth | pointed at internal-frontend (bypasses auth) | **OIDC login**, talks to the authorized external frontend |
| TLS | off (localhost) | mutual TLS on the frontend; TLS to Cloud SQL |
| Ingress | `kubectl port-forward` | internal load balancer / gRPC-capable ingress for the frontend; ingress for the UI |
| Metrics/dashboards | bundle kube-prometheus-stack | use the cluster's existing Prometheus + Grafana; apply only the ServiceMonitors + dashboards ([observability.md](../../docs/observability.md)) |

## Persistence values sketch

Replace the `20-temporal-values.yaml` datastore blocks with Cloud SQL. With
`passwordCommand`, no password Secret is created:

```yaml
server:
  config:
    persistence:
      numHistoryShards: 512   # still immutable — decide once
      datastores:
        default:
          sql:
            pluginName: postgres12
            databaseName: temporal
            connectAddr: "10.x.x.x:5432"   # Cloud SQL private IP
            connectProtocol: "tcp"
            user: temporal
            passwordCommand:
              command: /cloud-sql-token-helper   # emits a short-lived IAM token
            maxConns: 20
            maxConnLifetime: "1h"
        visibility:
          sql:
            pluginName: postgres12
            databaseName: temporal_visibility
            connectAddr: "10.x.x.x:5432"
            connectProtocol: "tcp"
            user: temporal
            passwordCommand:
              command: /cloud-sql-token-helper
```

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
