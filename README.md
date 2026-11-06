# K-Vision

A Kubernetes console with two faces on one engine:

- **`kv`** — a terminal UI in the spirit of k9s: keyboard only, fast, every resource
  type and CRD.
- **`kv ui`** — a desktop window in the spirit of Lens: tables, side panel, YAML,
  logs and an in-browser shell, with the same keys.

Both run on `internal/kube`, so they always agree on what a cluster contains, what
counts as unhealthy, and what a write is allowed to do.

## What it does that k9s and Lens don't

- **Every cluster at once.** `:all pods !ok` (terminal) or **All clusters** (desktop)
  lists one resource type across every kubeconfig context in parallel. A cluster
  that can't be reached (expired SSO, private endpoint) is reported on its own line
  and does not blank the view.
- **"Why?" for pods.** `x` / the *Why?* tab reads the pod's status and events and
  says what's wrong in a sentence. For example: *no node has enough cpu — check the
  NodePool limits*, *crash-looping, last exit 137 OOMKilled*, *cannot pull image*,
  or *exited with code 1 12d ago*.
- **First-class views for your own stack.** ArgoCD Applications (sync, health,
  revision), Karpenter NodePools and NodeClaims (nodes, CPU, limits), KEDA
  ScaledObjects and HPAs get real columns. Any CRD with a `Ready` condition is
  coloured, and Crossplane resources also show `NotSynced`.
- **Safe by default.** Deleting or applying (editing) a resource asks you to type its
  name and confirm. Scale, restart, cordon and drain ask for a confirmation click.
  Deleting several at once on a real cluster asks you to type `delete`. Every change is
  written to the audit log. Reading never asks: shells, logs and secret values open
  directly, and shells and secret reveals are recorded in the audit log.
- **Node shell.** `s` on a node, or the *Node shell* tab, gives a root shell on the
  host, the way `kubectl debug node/…` does: a short-lived privileged pod on that
  node (image `KV_NODE_SHELL_IMAGE`, default busybox from ECR Public, in
  `KV_NODE_SHELL_NAMESPACE`, default `default`), removed when you exit. Leftovers
  carry the label `k-vision/node-shell=true`.
- **Audit log.** Every write, exec and port-forward is appended to
  `~/.k-vision/audit.log` (JSON lines), including failures.
- **Small and offline.** One Go binary of about 30 MB, with no Electron and no
  network access beyond your clusters. The desktop UI's assets are embedded.

## Install

**See [INSTALL.md](INSTALL.md)** -- download from this project's **Deploy → Releases** and install on macOS,
Windows or Linux (and how to uninstall, update, or build from source). In short:

| | Install | Uninstall |
|---|---|---|
| **macOS** | unzip, then `./install.sh` (or drag from the `.dmg`) | `./install.sh --uninstall` |
| **Windows** | unzip, then `powershell -ExecutionPolicy Bypass -File install.ps1` | `… install.ps1 -Uninstall` |
| **Linux** | `tar xzf …`, then `./install.sh` | `./install.sh --uninstall` |

| | macOS | Windows | Linux |
|---|---|---|---|
| Terminal UI (`kv`) | ✓ | ✓ (Windows Terminal recommended) | ✓ |
| Desktop window (`kv ui`) | native app window | Edge / Chrome app window | Chrome / Chromium / Edge app window |
| Shells, logs, port-forward, files, cost, topology | ✓ | ✓ | ✓ |

It reads your kubeconfig exactly as kubectl does (`$KUBECONFIG`, then `~/.kube/config`).

## Filtering

One filter language in both UIs (`/` in the terminal, the filter box in the window).
Terms are separated by spaces and must all match.

| Filter | Matches |
|---|---|
| `repo` | name, namespace, status or any column contains it |
| `-kube-system` | a leading minus excludes |
| `"two words"` | quotes keep a phrase together |
| `!ok` · `is:failing` | unhealthy only · health is healthy/warning/failing/unhealthy |
| `ns:shop` · `status:crash\|error` | a field contains it; `\|` means or. Any column works (`node:`, `sync:`, `image:`) |
| `restarts>3` · `age<1h` | numeric and age compare (`> < >= <=`, units `s m h d w`) |
| `app=web` · `tier!=db` · `label:team` | label equals, differs, exists |

The window also has health tabs with counts (All, Failing, Warning, Healthy), quick
filters for each resource type ("Restarting", "Out of sync", "Cordoned"…), and chips
that show how each term was read. Keys `1` to `4` switch the health tab in both UIs.
The Go and browser versions are tested against the same cases
(`internal/kube/testdata/filter_cases.json`).

## Theme

System follows macOS light/dark, and switches live when macOS does.

The window has a **System / Light / Dark** switch at the bottom of the sidebar.
System follows macOS. The terminal UI adapts to your terminal's background;
`KV_THEME=light` or `KV_THEME=dark` forces one.

## Terminal: `kv`

`kv help` prints the full guide of commands, filters and keys, and `?` shows it inside the app.

```sh
kv                    # pods on the current context
kv deploy             # start on deployments
kv --context payments-dev nodes
```

| Key | Does |
|---|---|
| `:` | command, with autocomplete: Tab completes, ↑↓ picks. Any resource type (`po`, `deploy`, `app`, `nodepools`, `scaledobjects`…), `all <kind> [filter]`, `ctx <name>`, `ns <name>\|all`, `pf`, `audit`, `q` |
| `/` | filter as you type: text, `!ok` for unhealthy only, `label=value` |
| `j` `k` `g` `G` `PgUp` `PgDn` | move |
| `Enter` / `y` | YAML |
| `x` | why is this pod unhealthy |
| `l` / `p` | logs (follow) / logs of the previous, crashed container. `l` on a Deployment, StatefulSet, DaemonSet, ReplicaSet or Job, or with several pods marked, **merges every pod's logs** in time order, coloured per pod |
| `s` | shell: into a pod (bash, else sh), or on a **node** a root shell on the host |
| `v` | decoded values of a Secret or ConfigMap |
| `e` | edit in `$KUBE_EDITOR` / `$EDITOR`, applied on save after confirmation |
| `S` / `r` | scale / rollout restart |
| `f` | port-forward (`0:8080` picks a free local port) |
| `c` / `D` | cordon-uncordon / drain a node (evicts via the Eviction API, so PDBs hold) |
| `Space` / `Ctrl+A` | mark a row / mark every row shown (up to 100) |
| `Ctrl+D` | delete the row, or every marked row |
| `C` | cluster picker |
| `T` / `H` / `N` | Cluster Topology / Helm releases / create a resource from YAML |
| `n` / `0` | this row's namespace / all namespaces |
| `?` | all of the above |

In the log view: `/` find, `F` follow on/off, `P` previous container, `c` next
container, `w` save to `~/Downloads`.

Everything the window does, the terminal does too:

| Command | Does |
|---|---|
| `:topology` | **Cluster Topology**: node pools with every node (coloured by CPU requested), CPU/memory bars, and each namespace with the pools its pods run on. `+system` adds kube-system & co |
| `:topology <ns>` | **Namespace Topology** as a tree: Ingress → Service → workload (pods as dots, HPA/KEDA/PDB badges) → ConfigMaps/Secrets, missing ones in red |
| `:helm` · `:helm charts` | Helm releases / charts (no helm binary needed) |
| `:helm <release> [values\|all\|manifest\|notes\|history] [rev]` | one release in full |
| `:create [kind]` · `:create -f file.yaml` | create from YAML: a template opens in `$EDITOR`, the server validates it (dry run), then it is created after confirmation |
| `:ls [dir]` | browse a directory in the selected pod or node |
| `:cp <path> [local dir]` | copy a file (or a folder, as .tar) from the selected pod/node to `~/Downloads` |
| `:cp put <file> [dir]` | copy a file into the selected pod/node (`/tmp` by default) |
| `:overview <ns>` | Namespace Overview: every resource in it, grouped by kind |
| `:audit` | every change made from K-Vision |

Logs are coloured the same way as in the window (levels, timestamps, key=value, HTTP codes).

## Desktop: `kv ui`

Opens a window (Chrome app mode if Chrome is installed, otherwise your default
browser) on `http://127.0.0.1:7788`. The same keys work: `j`/`k`, `Enter`, `l`, `s`,
`x`, `e`, `Ctrl+D`, `/`, and `⌘K` or `:` for the command palette (every resource
type, every context, actions). Click a row for the side panel with **Why? · YAML ·
Logs · Shell**, and the actions the resource supports.

The server only listens on localhost. Every API call needs a random token generated
for that run, which is in the URL it opens. It refuses cross-origin requests and
foreign `Host` headers, so a web page you visit can't use it.

## Deleting several at once

Tick rows with the checkbox column (Shift-click selects a range, the header box
selects everything shown) or press `Space` on each. A bar shows **N selected** with
**Delete N…**. In the terminal, `Space` marks, `Ctrl+A` marks all shown and `Ctrl+D`
deletes the marked rows. A selection can span clusters in the all-clusters view.

- On safe clusters, one confirmation.
- On real clusters, type `delete` to confirm. The server checks it too.
- At most 100 at a time. Filter first, for example `status:error ns:payments`, then
  select all.
- Each object is backed up and audited like a single delete. If one fails, the rest
  still go ahead, the error is shown, and the failed ones stay selected.

## Start screen

Opening the app shows every cluster as a card, with a live check
of each one (Kubernetes version and node count, or why it can't be reached, such as
an expired login or a VPN-only endpoint). Type to search, use the arrow keys, press
Enter or click to open a cluster. **All clusters at once** opens the combined view.
Your recently opened clusters come first. Click **K-Vision** at the top of the
sidebar, or choose *Start screen* in ⌘K, to come back to it.

## Removing and deleting clusters

**Manage clusters** under the cluster list, or right-click any cluster, gives two
different actions:

| | What it does | kubectl | Undo |
|---|---|---|---|
| **Remove from list** (✕ on hover) | Hides it from K-Vision's lists only. Stored in `~/.k-vision/settings.json`. | still has it | **Put back** |
| **Delete from kubeconfig…** | Deletes it from `~/.kube/config`, like Lens or `kubectl config delete-context`. Its cluster and credentials go too, unless another context still uses them. | loses it | the backup in `~/.k-vision/kubeconfig-backups/` |

Delete always asks you to type the cluster's name. kubectl's current context can't
be removed or deleted. In the terminal, the cluster picker (`C`) has `h` for remove /
put back and `d` for delete.

## Creating resources

**＋ Create** in the top bar (or `N`, or ⌘K → Create) opens a YAML editor with a
template for the list you're on: Deployment, Pod, Service, Ingress, ConfigMap,
Secret, Job, CronJob, StatefulSet, DaemonSet, PVC, HPA, PDB, NetworkPolicy,
ServiceAccount, Role, RoleBinding, ResourceQuota, Namespace, or a skeleton for any
other kind. Edit it, or replace it with YAML you paste (several objects separated by
`---` are fine). **Validate** asks the API server to check it without creating
anything; **Create** (⌘↵) creates it. On a protected cluster you type the cluster's
name first. Every create is in the audit log.

## Copying files

The **Files** tab on a pod or a node works like `kubectl cp`: browse folders,
download a file (or a folder as `.tar`), and upload files into the folder you're in
with **Upload…** or by dropping them on the list. Pods need `tar` and `sh` in the
image (distroless images can't do this). Node files go through a short-lived helper
pod with the host's filesystem at `/host`. Uploading on a protected cluster asks for
the pod's or node's name. Every copy is in the audit log.

## Namespace Overview

**Namespace Overview** (top of Workloads, or ⌘K) is `kubectl get all -n <ns>`
done properly: every kind of resource that has something in the namespace, not just
the eight `get all` covers. That includes Secrets, ConfigMaps, PDBs, NetworkPolicies,
RBAC and every CRD (Argo CD Applications, TargetGroupBindings…). Each kind is a
section with its own columns, health and counts, plus a jump bar and a filter.
Events, endpoints and leases are left out unless you tick the box. In the terminal:
`:overview <namespace>` (or `:get-all`).

## Topology

The **Topology** section of the sidebar has two views (⌘K → "topology", or View in the menu bar):

- **Cluster Topology** — the whole cluster at a glance: totals (nodes, pods, CPU and
  memory requested, ingresses, namespaces that need a look), every node pool
  (Karpenter NodePool, EKS node group, Fargate) with each node drawn as a square coloured
  by how much CPU its pods request, and a band from each namespace to the pools its pods
  run on, as wide as the pod count. Platform namespaces (kube-system, monitoring…) are
  hidden unless you tick them. Click a namespace to open its topology.
- **Namespace Topology** — one namespace as the path traffic and configuration take:
  **Ingress → Service → workload → ConfigMap / Secret / volume**. Pods are dots inside
  their workload, coloured by health; ReplicaSets and a CronJob's Jobs are folded into
  their owner; HPAs, KEDA ScaledObjects and PDBs are badges ("PDB blocks drains" when it
  allows no disruption). Arrows are labelled *routes*, *selects*, *reads*, *mounts*.

Nothing is dimmed or blurred on the full map. **Click** a box to open its own view: only that resource and everything
connected to it, laid out **top to bottom** (Traffic → Services → Workloads → Config), with the selected one
highlighted; click another box there to move to it, **← All of <namespace>** or Esc to go back. Double-click opens it.
What's broken stands out: an Ingress whose backend Service doesn't exist, a Service
that selects no pods, a Secret or ConfigMap a workload needs but the namespace lacks
(red, dashed). References marked `optional: true` that are absent are grey, dashed.

### Helm Topology

**Topology → Helm Topology** (or the *Helm* switch in the map bar; `:topology helm` in the terminal) answers
*which Helm release created what*. Pick a release and see every resource in its manifest, grouped by kind
(traffic, workloads, config, RBAC, CRDs), each with its live state. Anything Helm installed that is no longer
in the cluster shows as **missing** in red, and a release is only green when everything it made is healthy.
Search finds a release by name, chart, or any resource it created. Click the release card for its values,
history and manifest; double-click a resource to open it.

## Argo CD, Pod Autoscaling, Node Autoscaling

The sidebar groups these by what they do:

| Section | Holds |
|---|---|
| **Argo CD** | **Dashboard** (apps by health and sync, what needs a look, recent syncs, by project / destination / repository, Application Sets), **Applications** (sync, health, project, repo, revision, destination, last sync), **Application Sets** (generators, how many apps they made), **Projects** |
| **Pod Autoscaling** | HPAs (target, min/max, current → desired replicas, metrics, *At max* when stuck at the ceiling), VPAs, KEDA ScaledObjects and ScaledJobs (with their triggers), TriggerAuthentications |
| **Node Autoscaling** | Karpenter NodePools, EC2NodeClasses (AMI, role, subnets, security groups), NodeClaims, NodeOverlays |

Sections only appear for what the cluster runs. The Dashboard is read from the cluster's Argo CD objects, so no
Argo CD login is needed. In the terminal: `:argo`.

## Cost

**Cost → Cost Overview** (⌘K → "cost"; `:cost` in the terminal) shows what the cluster costs, from the
cluster's own **OpenCost** (or Kubecost); K-Vision finds it and talks to it over a private port-forward,
nothing to configure. Pick a window (24h, 7 days, 30 days, this month) and group by namespace, workload, pod,
node or `team` label. Each row has a CPU / memory / storage / network breakdown, the monthly rate, how much
of the requested CPU and memory is really used, and **could save**: the cost of requested-but-unused
resources, which is what right-sizing requests would take off the bill. *Idle* is node cost nobody requested
(too-big or too-many nodes). Click a namespace to see its workloads. Answers are cached for ten minutes
(Refresh asks again). Clusters without OpenCost get a two-line install hint instead.

## Pod CPU and memory

The Pods list shows live **CPU** and **MEMORY** (from metrics-server, like `kubectl top`) with a bar for the share
of the pod's limit, or of its request when there's no limit. It turns amber at 70% and red at 90%, and a running pod
at 90% of its memory limit is marked as a warning, since that's where OOM kills start. Click the column to sort by it.
Without metrics-server in the cluster the columns show "—".

## Helm

The **Helm** section lists **Releases** (chart, version, app version, revision,
status, last update) and **Charts** (every chart deployed in the cluster, which
versions, and which releases use them). Open a release for its values, all values
(with the chart's defaults), manifest, notes and full revision history; click an
old revision to see what it was. It's read straight from the cluster, so it doesn't
need the `helm` binary.

## Resizing

Drag the edge between any two panes: the sidebar's right edge, the details panel's
left edge, or the top of the shell/logs dock. Sizes are remembered; double-click an
edge to reset it. Sidebar sections (Clusters, Workloads, Network…) fold and unfold
with a click and remember how you left them.

## Shells and logs in tabs

Shells and logs open as tabs in a dock along the bottom of the window. Each tab
keeps its own connection, so you can switch cluster or resource, or open more
tabs, without losing a session. Every tab carries its cluster's colour and name.
Drag the top edge to resize the dock; ▾ minimizes it.

- **Shell:** `s` on a pod or a node, or the Shell / Node shell button.
- **Logs of several pods:** tick pods and press **Logs of N** (or `l`), or use
  **Logs of all pods** on a Deployment, StatefulSet, DaemonSet, ReplicaSet or Job.
  Lines from every pod, even across clusters, are merged in time order, each pod
  in its own colour. Click a pod's chip to hide or show its lines. Up to 20 pods
  per tab.

### Split view

Shells and logs open as tabs in the bottom panel. With two or more open, **◫ Split** shows them all at once, side by
side, each with its own title bar and close button; **⇆ / ⇅** switches between side by side and stacked, and
**▭ Tabs** goes back. The choice is remembered. Merged logs of several pods have **◫ One per pod**, which opens each
pod in its own pane.

## Log colours

Logs are coloured by part: timestamps in grey-blue, a level badge (ERROR, WARN,
INFO, DEBUG), `key=value` keys in blue, quoted strings in green, numbers and
durations in amber, URLs underlined. Error lines get a red tint and a red edge,
warnings amber. JSON, logfmt and klog lines are all recognised.

## Configuration

| Env | Default | |
|---|---|---|
| `KV_SAFE` | local clusters only | Regexp of cluster names that are **not** protected (replaces the default) |
| `KV_AUDIT_LOG` | `~/.k-vision/audit.log` | Where writes are recorded |
| `KUBE_EDITOR`, `EDITOR` | `vi` | Editor for `e` in the terminal |

## Layout

```
cmd/kv            the binary: terminal UI, or `kv ui`
internal/kube     clusters, resources, health rules, writes + audit, logs/exec/port-forward, explain
internal/tui      Bubble Tea terminal UI
internal/web      localhost API + embedded single-page UI (static/)
```

`make test` runs the unit tests. They need no cluster: they cover the health rules,
the explainer, the protected-context check and the server's token, Origin, Host and
confirmation checks.

## Roadmap

- Metrics (CPU/memory per pod and node, via metrics-server) and sparklines.
- Relationship view: Ingress → Service → Pods, Deployment → ReplicaSets → Pods.
- "Investigate with DevOps Agent": send a pod's status, events and logs to the
  AgentSpace bridge.
- Watch-based updates (informers) instead of 5-second polling.
- Native window (Wails) instead of the browser app window.
