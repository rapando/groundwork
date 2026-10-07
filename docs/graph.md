# Graph, Visualize and drift

## Dependency graph (Graph screen)

Pick a stack (root + environment). Resources are nodes; an arrow points from a dependency to what uses it, labelled with the argument that holds the reference. `count`/`for_each` instances collapse into one node with ×N, where N is resolved from the environment's tfvars and module arguments when possible (×n otherwise).

The model comes from **static analysis of the HCL**: it needs no `terraform init` and no credentials. References are followed through module variables and outputs, so an edge from a root resource into a module's resource is exact. When the root has been initialised, edges from `terraform graph` are merged in. Overlays: the newest plan waiting for approval (create / update / replace / destroy) and recorded drift (dashed amber). **Modules** collapses the graph to one node per module. Export as SVG or PNG.

## Architecture (Code screen → Split / Visualize)

For `.tf` files the Code screen can show what the code builds: network resources (`aws_vpc`, `google_compute_network`, `azurerm_virtual_network`, `hcloud_network`, `digitalocean_vpc`) are drawn as containers, and resources that reference them, directly or through a subnet-like argument (`vpc_id`, `subnet_id(s)`, `subnetwork`, `network_id`, …), sit inside. The rules are data (`internal/graph/rules.yaml`); resources that relate to nothing drawn are listed as "not placed" rather than guessed.

It is computed from the **unsaved editor buffer** (debounced 400 ms), so it follows your typing. The block under the cursor is outlined; clicking a box jumps to its definition; blocks with diagnostics get a red dashed border; resources outside the current file's module are dimmed. "As used by" picks which root/environment's values resolve counts. A file with syntax errors still shows what parses.

## Drift

**Detect drift** runs `terraform plan -refresh-only` as a read-only job (it takes the root's lock, needs no approval, and the refresh-only plan is deleted, never applied). Drifted attributes are stored per root/environment (values masked like plan diffs) and shown in the Graph inspector as *in state/code* vs *actual*, on the Overview environment card, and in the sidebar.

Three actions per drifted attribute:

| Action | What happens |
|---|---|
| Copy into code | Sets the argument to the real value in the resource's block (top-level string/number/bool arguments only). |
| Ignore | Adds the attribute to `lifecycle { ignore_changes = [...] }`, creating the block if needed. |
| Revert with apply | Starts an ordinary plan; review and approve it as usual. |

Code edits go through `hclwrite` (comments and formatting are kept), are shown as a diff first, and are written only if the file hasn't changed since the preview. A resource deleted outside Terraform can only be reverted (the plan recreates it).

A drift schedule (`drift.schedule` in `groundwork.yaml`) arrives with Doctor and notifications.
