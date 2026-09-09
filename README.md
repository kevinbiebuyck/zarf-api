# zarf-api

An HTTP API over [zarf](https://zarf.dev) that runs **inside** your Kubernetes
cluster: import zarf packages over HTTP, then deploy, list and remove them in
that cluster — without needing the zarf CLI on the client.

## Design: a thin shell over the zarf CLI's own code

The [`zarf`](./zarf) directory is a **git submodule** pinning
[zarf-dev/zarf](https://github.com/zarf-dev/zarf). The API does not reimplement
anything; it calls the exact same package-level entrypoints the zarf CLI calls
(`src/cmd/package.go` → `src/pkg/packager`), which is the most stable contract
zarf has:

| API operation | zarf code path used |
| --- | --- |
| import / validate | `layout.LoadFromTar` |
| deploy | `packager.LoadPackage` + `packager.Deploy` (mirrors `zarf package deploy --confirm`) |
| remove deployed | `packager.GetPackageFromSourceOrCluster` + `packager.Remove` (mirrors `zarf package remove --confirm`) |
| list deployed | `cluster.GetDeployedZarfPackages` (mirrors `zarf package list`) |

Upgrading zarf = `git submodule update --remote zarf && go mod tidy && rebuild`.

The Go module uses a `replace github.com/zarf-dev/zarf => ./zarf` directive, so
builds always compile against the pinned submodule commit.

## Security

**This API performs no authentication or authorization.** It is designed to
sit behind an APIM / API gateway that authenticates callers and authorizes
each operation. Anyone who can reach the service port can deploy arbitrary
packages into the cluster.

## API overview

Base path: `/api/v1`

### Local package store

| Method & path | Description |
| --- | --- |
| `POST /api/v1/packages` | One-shot import. Body = raw `.tar.zst`/`.tar`. Query: `fileName`, `shasum`, `validate=false`, `verify=never\|if-possible\|always` |
| `GET /api/v1/packages` | List imported packages |
| `GET /api/v1/packages/{id}` | Package metadata |
| `DELETE /api/v1/packages/{id}` | Delete from the local store |
| `POST /api/v1/packages/{id}/deploy` | Deploy into the cluster (async job, `?wait=true` for sync) |

### Chunked import (for large packages)

| Method & path | Description |
| --- | --- |
| `POST /api/v1/uploads` | Start a session. Body: `{"fileName": "...", "sha256": "..."}` (both optional) |
| `PUT /api/v1/uploads/{id}/chunks/{n}` | Push chunk `n` (0-based, any order, raw bytes) |
| `GET /api/v1/uploads/{id}` | Session status (received chunks/bytes) |
| `POST /api/v1/uploads/{id}/complete` | Assemble chunks, verify sha256, validate with zarf, store |
| `DELETE /api/v1/uploads/{id}` | Abort and clean up |

Chunks must be contiguous from `0` at completion. Imported packages are
validated by loading them with zarf itself (checksums + optional signature
verification), so a corrupt or tampered package fails at import time, not at
deploy time.

### Cluster operations

| Method & path | Description |
| --- | --- |
| `GET /api/v1/deployments` | List deployed packages (zarf state secrets) |
| `GET /api/v1/deployments/{name}` | Full recorded state of one deployment |
| `DELETE /api/v1/deployments/{name}` | Remove from cluster (async job). Query: `components`, `namespaceOverride`, `timeout`, `skipVersionCheck` |
| `POST /api/v1/deployments/{name}/remove` | Same, with JSON body options |
| `GET /api/v1/jobs` / `GET /api/v1/jobs/{id}?tail=100` | Job status + captured zarf logs |

### Deploy request body

Mirrors `zarf package deploy` flags:

```json
{
  "components": "optional-a,optional-b",
  "setVariables": {"DOMAIN": "example.com"},
  "setValues": {"replicas": "3"},
  "values": {"image": {"pullPolicy": "Always"}},
  "namespaceOverride": "",
  "takeOwnership": false,
  "connected": false,
  "forceConflicts": false,
  "timeout": "15m",
  "retries": 3,
  "ociConcurrency": 3,
  "shasum": "...",
  "verify": "if-possible",
  "publicKeyPath": "/keys/cosign.pub",
  "skipValuesSchemaValidation": false,
  "skipVersionCheck": false
}
```

`source` (optional) overrides the stored package with anything the CLI accepts
as `PACKAGE_SOURCE` (`oci://...`, `https://...`, local path).

Deploys and removes run as **jobs** (serialized — zarf uses process-global
state). The response is `202 Accepted` with a job id; poll
`GET /api/v1/jobs/{id}` for status and the zarf logs, or add `?wait=true` to
block until completion.

## Running

### Locally (needs a kubeconfig pointing at a cluster)

```sh
git submodule update --init --recursive
go run ./cmd/zarf-api
```

### Docker

```sh
git submodule update --init --recursive
docker build -t zarf-api .
```

### Kubernetes (Helm)

```sh
helm upgrade --install zarf-api ./charts/zarf-api -n zarf-api --create-namespace
```

The chart creates a ServiceAccount with `cluster-admin` by default — zarf
installs arbitrary namespaces/CRDs/webhooks on behalf of packages, so anything
narrower must be tailored to your packages (`rbac.clusterAdmin=false` +
`rbac.extraRules`). A PVC backs the package store and zarf cache.

## Configuration (env)

| Variable | Default | Description |
| --- | --- | --- |
| `ZARF_API_PORT` | `8080` | Listen port |
| `ZARF_API_DATA_DIR` | `./data` (`/data` in the image) | Packages, uploads, cache |
| `ZARF_API_TEMP_DIR` | `<dataDir>/tmp` | Extraction scratch space |
| `ZARF_API_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `ZARF_API_LOG_FORMAT` | `json` | `json`, `console`, `dev` |
| `ZARF_API_PUBLIC_KEY_PATH` | — | Default cosign key for signature verification |
| `ZARF_API_MAX_UPLOAD_SESSIONS` | `16` | Concurrent chunked uploads |
