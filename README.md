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

## Web UI

A simple embedded web UI is served at `/ui/` (redirect from `/`) when
`ZARF_API_UI_ENABLED=true` (default; Helm: `ui.enabled`). It supports:

- importing packages (chunked upload with progress, pause/resume) and
  deleting them
- browsing the local store grouped by application with all loaded versions
- listing deployed packages, and for each: **Edit** (redeploy with changed
  helm values/components), **Upgrade** (to another version present in the
  store), **Delete**, plus **New installation** from any stored package
- generating the install configuration form from a package's
  `config.schema.json` when present (see below)
- watching deploy/remove jobs with their captured zarf logs

Disable it with `ZARF_API_UI_ENABLED=false` to expose only the JSON API.

## Security

**This API performs no authentication or authorization.** It is designed to
sit behind an APIM / API gateway that authenticates callers and authorizes
each operation. Anyone who can reach the service port can deploy arbitrary
packages into the cluster. The same applies to the web UI — if you expose it,
protect it.

## API overview

Base path: `/api/v1`

### Local package store

| Method & path | Description |
| --- | --- |
| `POST /api/v1/packages` | One-shot import. Body = raw `.tar.zst`/`.tar`. Query: `fileName`, `shasum`, `validate=false`, `verify=never\|if-possible\|always` |
| `GET /api/v1/packages` | List imported packages |
| `GET /api/v1/packages/{id}` | Package metadata |
| `GET /api/v1/packages/{id}/definition` | Package definition (variables, components) — `zarf package inspect definition` |
| `GET /api/v1/packages/{id}/config-schema` | The package's `config.schema.json` (JSON Schema for install-time configuration), `404` when absent |
| `DELETE /api/v1/packages/{id}` | Delete from the local store |
| `POST /api/v1/packages/{id}/deploy` | Deploy into the cluster (async job, `?wait=true` for sync) |

### Chunked import (for large packages)

| Method & path | Description |
| --- | --- |
| `POST /api/v1/uploads` | Start a session. Body: `{"fileName": "...", "sha256": "..."}` (both optional) |
| `GET /api/v1/uploads` | List in-flight sessions (for resume/cleanup) |
| `PUT /api/v1/uploads/{id}/chunks/{n}` | Push chunk `n` (0-based, any order, raw bytes; re-sends allowed) |
| `GET /api/v1/uploads/{id}` | Session status (received chunks/bytes) |
| `POST /api/v1/uploads/{id}/complete` | Assemble chunks, verify sha256, validate with zarf, store |
| `DELETE /api/v1/uploads/{id}` | Abort and clean up |

Uploads are resumable: chunks are stored atomically per index, so a client
can query the session, skip chunks the server already has, and continue. The
web UI uses this for pause/resume and wipes other sessions when a new upload
starts.

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
  "valuesOverrides": {"component-name": {"chart-name": {"replicaCount": "3"}}},
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
  "skipVersionCheck": false,
  "deletePackageAfterDeploy": false
}
```

`deletePackageAfterDeploy` (not a zarf flag — an API convenience) removes the
package from the local store once the deploy **succeeded**; the job result
then reports `"packageDeleted": true`. The deployment stays fully managed
from cluster state (remove/inspect keep working); re-upload the package to
edit it later. A failed cleanup only logs a warning, it never fails the
deploy.

`source` (optional) overrides the stored package with anything the CLI accepts
as `PACKAGE_SOURCE` (`oci://...`, `https://...`, local path).

Note the difference between the two helm-values mechanisms, mirroring zarf
itself: `setValues`/`values` populate the package-level values document (like
`--set-values`) and only reach a chart when the package maps them via chart
`values` sourcePath/targetPath entries. `valuesOverrides` are direct per-chart
overrides (component → chart → dot-path → value) merged on top of everything
else. String leaves are typed by inference (`"3"` → `3`, `"true"` → `true`,
wrap in single quotes to force a string); non-string JSON values (bool,
number, array) are used as-is. This is what the UI's values forms send.

### Schema-driven configuration (`config.schema.json`)

When a package ships a **`config.schema.json`** (JSON Schema) at the root of
its tarball, the deploy/edit/upgrade modal replaces the free-form values
editor with a **form generated from that schema** — strings, numbers,
booleans, `enum` dropdowns, nested objects and comma-separated arrays, with
`default` prefill, `description` hints and `required` markers. On submit the
values are sent as `valuesOverrides` applied to **every helm chart of the
components being deployed** (helm harmlessly ignores keys a chart doesn't
use).

Packaging caveat: `zarf package create` only writes files it knows about, so
a `config.schema.json` sitting next to your `zarf.yaml` is **not** included
automatically. Add it to the tarball after create, register it in
`checksums.txt` and update `metadata.aggregateChecksum` in `zarf.yaml`,
otherwise the package fails integrity validation. The
[`tools/addschema`](./tools/addschema/main.go) helper does exactly that:

```sh
go run ./tools/addschema zarf-package-myapp-amd64-1.0.0.tar.zst config.schema.json out.tar.zst
```

Note: modifying `zarf.yaml` breaks package signatures — inject before
signing, or re-sign afterwards.

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
| `ZARF_API_UI_ENABLED` | `true` | Serve the web UI at `/ui/` |
| `ZARF_API_UI_CUSTOM_COLUMNS` | — | JSON array configuring extra columns in the Installed tab (e.g. `[{"header": "Col", "path": "connectStrings.my-col"}]`) |
| `ZARF_API_BASE_PATH` | — | URL prefix for the API and UI (e.g. `/zarf-api`) when behind a path-routing reverse proxy; the proxy must forward the prefix unstripped. Probes stay at `/healthz` and `/readyz` |
| `ZARF_API_MAX_UPLOAD_SESSIONS` | `16` | Concurrent chunked uploads |
