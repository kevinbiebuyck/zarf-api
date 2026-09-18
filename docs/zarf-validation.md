# Zarf validation — kind cluster

Date: 2026-09-18. All tests below were executed for real on a local kind
cluster; every source artifact (package sources, manifests, built tarballs) is
kept in the git-ignored `e2e-out/validation/` directory.

## Test environment

| Item | Value |
|---|---|
| Cluster | kind `zarf-api-e2e`, Kubernetes v1.34.0, single control-plane node, Docker 29.5.2 |
| Zarf init | v0.68.0 (components: injector, seed-registry, registry, agent — **no git server**) |
| Zarf library (zarf-api submodule) | v0.85.0 |
| Deploys driven through | `zarf-api` (this repo) running locally on :8080 against the kind kubeconfig |
| Test app | podinfo 6.7.1 (helm chart), plus purpose-built demo packages |

Cluster content after all tests: zarf system (registry + agent), podinfo
(3 replicas, ns `podinfo`), ArgoCD (ns `argocd`), guestbook via ArgoCD
(ns `guestbook`), templ-demo (ns `templ-demo`), zarf-api (ns `zarf-api`),
metrics-server.

---

## 1. ArgoCD + Zarf

### What was tested

1. Package ArgoCD itself as a zarf package and deploy it (airgap-style
   delivery of a GitOps engine).
2. Have ArgoCD deploy and manage an application (guestbook from a public git
   repo) inside the zarf-managed cluster.
3. Make the ArgoCD-managed workload pull its image from the **zarf internal
   registry** (the real airgap loop).

### 1.1 ArgoCD as a zarf package — works

Package source: `e2e-out/validation/argocd/zarf.yaml` — helm chart
`argo-cd` 8.0.9 from `https://argoproj.github.io/argo-helm`, dex /
notifications / applicationSet disabled for a minimal footprint. Images were
discovered with zarf's own tooling:

```sh
zarf dev find-images e2e-out/validation/argocd
# -> public.ecr.aws/docker/library/redis:7.2.8-alpine
# -> quay.io/argoproj/argocd:v3.0.3
```

Created with `zarf package create`, uploaded and deployed through zarf-api
(`POST /api/v1/packages`, `POST /api/v1/packages/{id}/deploy?wait=true`).
Result: all ArgoCD pods Running in ~18 s of deploy job time; package size
**194.6 MB** (2 images + chart + SBOMs).

Proof the airgap mechanism is active: the ArgoCD server **pod** image was
rewritten by the zarf agent at admission time to the internal registry:

```
deployment spec:  quay.io/argoproj/argocd:v3.0.3          (unchanged)
pod spec:         127.0.0.1:31999/argoproj/argocd:v3.0.3-zarf-2331512642
```

UI access: `kubectl port-forward svc/argo-cd-argocd-server 8443:443 -n argocd`,
user `admin`, password from secret `argocd-initial-admin-secret`.

### 1.2 ArgoCD deploying an app in a zarf cluster — works, with two gotchas

Application used: `e2e-out/validation/argocd/guestbook-app.yaml`
(argocd-example-apps guestbook, automated sync, CreateNamespace).

**Gotcha 1 — the zarf agent mutates ArgoCD Applications.** The agent ships
dedicated webhooks for ArgoCD CRs (`/mutate/argocd-application`,
`-applicationset`, `-appproject`, `argocd-repository`). Its purpose: rewrite
`spec.source.repoURL` towards the zarf-hosted git server. Since this cluster
was initialized **without the git-server component**, the rewrite produced an
invalid URL and the webhook call failed:

```
ERR unable to transform the git url: unable to get extract the repo name
    from the url //argocd-example-apps-861626273.git
# ArgoCD controller side:
Error persisting normalized application spec: failed calling webhook
"agent-argocd-application.zarf.dev" ... -> app stuck at sync=Unknown
```

Fix (documented by zarf itself: *"resources deployed indirectly, for instance
through gitops, should set this label"*): opt the Application out of mutation.

```yaml
metadata:
  labels:
    zarf.dev/agent: ignore
```

With the label: `sync=Synced` immediately. (The label is checked on the object
first, then the namespace; `mutate`/`ignore`/`skip` values, default policy
comes from zarf state.)

**Gotcha 2 — pod images get rewritten, then fail to pull.** The guestbook pod
was mutated to `127.0.0.1:31999/google-samples/gb-frontend:v5-zarf-...` which
did not exist in the registry → `ImagePullBackOff`. Two ways out:

- *Connected mode:* label the target namespace `zarf.dev/agent=ignore` so
  images are pulled from the internet as-is.
- *Airgap mode (validated):* deliver the image with an **image-only zarf
  package** (`e2e-out/validation/guestbook-images/zarf.yaml`, one component,
  one image). Deploy pushes the image into the zarf registry with exactly the
  name the agent's transform computes. One more piece is required: the
  namespace must contain the registry pull secret — zarf creates a
  `kubernetes.io/dockerconfigjson` secret named **`private-registry`** in every
  namespace it deploys into, but ArgoCD-created namespaces don't have it.
  Copy it and attach it to the service account:

  ```sh
  kubectl get secret private-registry -n argocd -o json | ... # retarget to guestbook
  kubectl patch sa default -n guestbook -p '{"imagePullSecrets":[{"name":"private-registry"}]}'
  ```

After the image package deploy + pull secret: pod Running, application
`Synced / Healthy` (verified via ArgoCD API).

### 1.3 Coexistence model — who owns what

| Concern | Owner | Notes |
|---|---|---|
| Cluster bootstrap (registry, agent, CA) | zarf init | one-time |
| ArgoCD installation | zarf package | airgap-deliverable, versioned, reproducible |
| Application manifests (desired state) | ArgoCD | git repo is the source of truth |
| Application images (airgap) | zarf image packages | agent rewrite + `private-registry` pull secret |
| Zarf-deployed apps | zarf | ArgoCD *sees* the resources but cannot adopt zarf's helm releases; don't manage the same workload with both |

If the cluster is initialized **with** the git-server component (gitea), the
agent rewrites ArgoCD repoURLs to gitea instead of failing — that is the
intended full-airgap design (push your app repos to gitea, ArgoCD follows
them). Not tested here (init without git server).

---

## 2. Resource usage (measured)

`kubectl top` after all deployments, cluster otherwise idle:

### Zarf's own footprint (ns `zarf`)

| Pod | CPU | Memory |
|---|---|---|
| agent-hook × 2 | 1m each | 23Mi each |
| zarf-docker-registry | 1m | 45Mi |
| **Total zarf overhead** | **~3m** | **~91Mi** |

Registry PVC: **628 MiB** used for 7 mirrored images (incl. ArgoCD, podinfo,
gb-frontend) + their SBOM artifacts, on a 20 Gi PVC.

### Everything else

| Namespace / workload | CPU | Memory |
|---|---|---|
| argocd (5 pods: server, repo-server, controller, applicationset, redis) | ~10m | ~163Mi |
| zarf-api | 1m | 22Mi |
| podinfo × 3 | ~9m | ~55Mi |
| guestbook | 1m | 10Mi |
| kube-system (apiserver, etcd, …) + metrics-server | ~100m | ~780Mi |
| **Whole node** | **208m (1%)** | **1953Mi (4%)** |

Package sizes on disk: argocd 194.6 MB · guestbook-images 324.7 MB (single
large legacy image) · podinfo 29.7 MB · templ-demo 0.3 MB (no images).

Takeaway: zarf's standing cost is negligible (~3m CPU / ~100Mi RAM + registry
storage). ArgoCD costs ~5× that. The dominant storage factor is image size,
not zarf metadata.

---

## 3. What zarf actually buys us (objectified)

Measured/observed during this validation:

1. **Single-file airgap delivery.** One `.tar.zst` carries chart(s) + images +
   manifests + SBOMs + checksums (podinfo: 29.7 MB; ArgoCD: 194.6 MB).
   Deploying it needs zero internet access from the cluster side — proven by
   the agent rewriting ArgoCD's images to the internal registry and the pods
   running from it.
2. **Integrity by construction.** Every file is checksummed
   (`checksums.txt` + aggregate in `zarf.yaml`); a hand-modified tarball is
   rejected at import (verified earlier with `config.schema.json` injection).
   Optional cosign signing.
3. **The agent = deploy-time image indirection.** Any pod created in a mutated
   namespace is transparently redirected to the internal registry — including
   workloads zarf didn't deploy (guestbook via ArgoCD). This is what makes
   "GitOps inside an airgap" feasible at all.
4. **GitOps enabler, not GitOps engine.** Zarf deliberately does not do
   continuous reconciliation; it delivers the *means* (registry, optional
   gitea, ArgoCD itself) and gets out of the way. The combination
   zarf (delivery) + ArgoCD (reconciliation) covers the full lifecycle.
5. **Templating with guardrails.** Package values are validated against a JSON
   Schema at deploy time (a bad enum value was rejected before anything
   touched the cluster — see §4).
6. **State introspection.** Every deployment is a secret in the `zarf`
   namespace holding the full package definition, deployed components, connect
   strings, generation counter — which is what powers
   `GET /api/v1/deployments` and the UI.

What we want from zarf, concretely: **(a)** reproducible, auditable delivery
of an app stack as one artifact; **(b)** no runtime dependency on the internet
or external registries; **(c)** a controlled configuration surface (schema-
validated values); **(d)** compatibility with a GitOps operating model
(ArgoCD) once the cluster is up.

---

## 4. Templatisation

Demo package: `e2e-out/validation/templ-demo/` (a ConfigMap manifest + a tiny
local helm chart, no images). One deploy exercised every mechanism at once:

```json
{
  "setVariables": {"ENV_NAME": "prod"},
  "values": {"demo": {"greeting": "hi from deploy values"}},
  "valuesOverrides": {"demo-chart": {"demo": {"count": 7}}}
}
```

Resulting cluster state:

| Mechanism | Source | Result |
|---|---|---|
| Variable `###ZARF_VAR_ENV_NAME###` in manifest | `setVariables` (default `dev` overridden) | `env: prod` |
| Constant `###ZARF_CONST_SITE###` in manifest | fixed at create time | `site: kind-local` |
| Package values file `values.yaml` (`count: 2`) | packaged defaults | overridden, see below |
| Deploy-time `values` (`greeting`) | merged over package values file | `greeting: hi from deploy values` |
| `valuesOverrides` (`count: 7`) | direct per-chart, top of stack | `count: 7` |

**Proven precedence:** chart default (1) < package values file (2) <
deploy-time values (not set for count) < valuesOverrides (7).

Schema validation: the package ships `values.schema.json` (declared natively
via `values.schema` in zarf.yaml — `zarf package create` includes and checksums
it automatically). A deploy with `logLevel: "bogus"` failed server-side before
any cluster change:

```
values validation failed: schema validation failed: logLevel must be one of
"info", "debug", "warn"
```

Chart mapping: package values reach a chart only through explicit
`values: [{sourcePath, targetPath}]` entries on the chart (templ-demo maps the
`.demo` subtree to the chart root). The zarf-api UI renders the schema as a
form and submits typed package values.

---

## 5. Same package deployed multiple times (multi-namespace)

Test: podinfo 1.2.0 already deployed in ns `podinfo` (3 replicas); deploy the
**same package** again with `{"namespaceOverride": "podinfo-b", "values":
{"replicaCount": 1, "ui": {"color": "#aa3333", "message": "instance B"}}}`.

Observed:

- Works. Both instances run side by side (3 pods in `podinfo`, 1 in
  `podinfo-b`), each with its own values (`PODINFO_UI_MESSAGE=instance B` on B,
  A untouched).
- Zarf tracks state **per name+override**: secrets `zarf-package-podinfo` and
  `zarf-package-podinfo-override-podinfo-b`, with independent generation
  counters (23 vs 1). Both show up in `GET /api/v1/deployments` (the second
  carries `"namespaceOverride": "podinfo-b"`).
- Helm releases are namespaced, so both are named `podinfo` without collision.
- Targeted remove works: `DELETE /api/v1/deployments/podinfo?namespaceOverride=podinfo-b`
  removed only instance B (pods, helm release, state secret). The now-empty
  namespace itself is **not** deleted by zarf.

Caveats:

- Both instances appear as "podinfo" in listings — use a custom column on
  `namespaceOverride` (or `deployedComponents.0.installedCharts.0.namespace`)
  in the UI to tell them apart.
- The UI deploy modal does not expose `namespaceOverride` (API-only), and the
  UI Delete button doesn't pass it — it would target the primary instance.
- Constraint from zarf: `namespaceOverride` is refused when the package
  contains multiple distinct chart/manifest namespaces (or sets
  `preventNamespaceOverride`).
- Alternative pattern for many instances: one package per instance (name
  templated at create time), or ArgoCD with an ApplicationSet — zarf then only
  delivers images.

---

## 6. Reproduce

All sources live in `e2e-out/validation/` (git-ignored):

```
e2e-out/validation/
  templ-demo/            # zarf.yaml + values.yaml + values.schema.json + cm.yaml + chart/
  argocd/                # zarf.yaml + values.yaml + guestbook-app.yaml
  guestbook-images/      # zarf.yaml (image-only package)
  *.tar.zst              # built packages
```

Typical flow (CLI for create, API for everything else):

```sh
zarf package create e2e-out/validation/<pkg> -o e2e-out/validation --confirm
curl -X POST --data-binary @e2e-out/validation/<file>.tar.zst \
  "http://localhost:8080/api/v1/packages?fileName=<file>.tar.zst"
curl -X POST -H 'Content-Type: application/json' -d '{"values":{...}}' \
  "http://localhost:8080/api/v1/packages/<id>/deploy?wait=true"
```

## 7. Open points / recommendations

- **Init with git-server** if ArgoCD should consume zarf-hosted git repos
  (removes the need for the `zarf.dev/agent: ignore` label on Applications).
- Standardize a small **namespace bootstrap** for GitOps targets:
  `zarf.dev/agent` label decision + `private-registry` pull secret (could be
  an ArgoCD sync-wave-0 resource or a zarf package).
- Decide the ownership boundary per workload (zarf-deployed vs
  ArgoCD-managed) and stick to it — both are deployers.
- zarf-api UI: surface `namespaceOverride` if multi-instance becomes a
  supported workflow (list column exists; deploy/remove UI doesn't pass it).
