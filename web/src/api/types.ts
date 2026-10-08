export interface Tool { name: string; found: boolean; path?: string; version?: string; hint?: string }

export interface Workspace {
  root: string
  name: string
  version: string
  configured: boolean
  git: boolean
  tools: Tool[]
  setup: 'none' | 'detect' | 'scaffold'
  mode?: 'standalone' | 'embedded'
  config?: Record<string, any>
  config_error?: string
}

export interface Evidence { file: string; line?: number; note: string }
export interface TFDir {
  path: string; kind: 'root' | 'module'; score: number; backend?: string
  providers?: string[]; tfvars?: string[]; referenced_by?: string[]; evidence: Evidence[]
}
export interface AnsibleProject {
  path: string; config?: string; playbooks?: string[]; roles?: string[]
  inventories?: string[]; vars_dirs?: string[]; evidence: Evidence[]
}
export interface CIRef { file: string; line?: number; tool: string; working_dir?: string }
export interface Env { name: string; terraform?: string[]; ansible?: string[] }
export interface Report {
  mode: 'standalone' | 'embedded'; empty: boolean; file_count: number; truncated?: boolean
  iac_ratio: number
  terraform_roots: TFDir[]; terraform_modules: TFDir[]; ansible: AnsibleProject[]
  ci: CIRef[]; envs: Env[]
}
export interface DetectResponse { report: Report; config_yaml: string; exists: boolean }

export interface ScaffoldForm {
  preset: 'aws-envs' | 'onprem'
  project: string
  backend: 's3' | 'local' | 'http'
  bucket: string
  region: string
  envs: string[]
  layout: 'dirs' | 'workspaces'
  inventory: 'terraform' | 'static'
  roles: string[]
  vault: boolean
  checks: string[]
  pre_commit: boolean
  ci: boolean
}
export interface PlannedFile { path: string; content: string; executable?: boolean; merge?: boolean; exists: boolean }
export interface PreviewResponse { files: PlannedFile[]; count: number; existing: number; new: number }
export interface WriteResult { path: string; status: 'created' | 'overwritten' | 'merged' | 'skipped' }

export interface Edit { file: string; line: number; col: number; end_line: number; end_col: number; old: string; new: string }
export interface QuickFix { title: string; kind: 'edit' | 'fmt'; edits?: Edit[]; file?: string }
export interface Diagnostic {
  id: number; unit: string; tool: string; severity: 'error' | 'warning' | 'info'
  code?: string; message: string; detail?: string; file: string
  line?: number; col?: number; end_line?: number; end_col?: number
  link?: string; fix?: QuickFix
}
export interface ToolStatus {
  tool: string; status: 'idle' | 'running' | 'ok' | 'issues' | 'skipped' | 'failed'
  count: number; message?: string; hint?: string; duration_ms?: number; ran_at?: string
}
export interface UnitStatus { id: string; kind: string; path: string; tools: string[]; running: boolean; results: ToolStatus[] }
export interface ChecksResponse {
  diagnostics: Diagnostic[]; units: UnitStatus[]; counts: { error: number; warning: number; info: number }
}

export interface Section { name: string; kind: 'terraform' | 'ansible'; base: string }
export interface FilesResponse { files: string[]; sections: Section[]; truncated: boolean; total: number }
export interface FileContent { content: string; sha: string; mtime: string; size: number }

export interface GitFile { path: string; status: string; x: string; y: string; orig?: string }
export interface GitStatus { available: boolean; branch?: string; head?: string; ahead?: number; behind?: number; files: GitFile[] }

// ---- runs ----
export interface RunTarget {
  tool?: 'terraform' | 'ansible'; root?: string; env: string; workspace?: string; approval_required: boolean; binary?: string; var_files?: string[]
  project?: string; inventory?: string; playbook?: string; limit?: string; tags?: string; skip_tags?: string
  pattern?: string; module?: string; args?: string
}
export interface PlanChange {
  address: string; type: string; name: string; module?: string
  action: 'create' | 'update' | 'replace' | 'delete' | 'read'; replace_paths?: string[][]; reason?: string
}
export interface PlanSummary {
  create: number; update: number; replace: number; delete: number; read: number
  changes: PlanChange[]; applyable: boolean
}
export interface ApplySummary { added: number; changed: number; destroyed: number; outputs?: string[] }
export interface RunBrief {
  id: number; kind: string; status: RunStatus; stage: string; commit?: string
  created_at: string; started_at?: string; ended_at?: string; exit_code?: number
  target: RunTarget; no_changes: boolean; error: string; apply?: ApplySummary; drift?: { resources: number; attrs: number }
  counts?: { create: number; update: number; replace: number; delete: number }
}
export type RunStatus = 'queued' | 'running' | 'waiting_approval' | 'succeeded' | 'failed' | 'cancelled'
export interface RunStage { name: string; status: 'pending' | 'running' | 'succeeded' | 'failed' | 'skipped' | 'cancelled'; started_at?: string; ended_at?: string; detail?: string }
export interface RunDetail {
  run: RunBrief & { argv: string[]; summary: unknown }
  target: RunTarget
  stages: RunStage[]
  summary: { plan?: PlanSummary; plan_sha256?: string; no_changes?: boolean; apply?: ApplySummary; error?: string; drift?: { resources: number; attrs: number } }
  approval?: { approved_by: string; approved_at: string; confirm_text: string }
}
export interface ResEvent { address: string; action: string; state: 'running' | 'done' | 'failed'; elapsed?: number }
export interface LogLine { n: number; t: string; stage: string; level: 'debug' | 'info' | 'warn' | 'error'; text: string; res?: ResEvent }
export interface LogPage { lines: LogLine[]; next: number; total: number }

export interface EnvTarget {
  root: string; env: string; approval_required: boolean; running: boolean
  last_run?: RunBrief; in_sync_since?: string
  pending?: { run_id: number; create: number; update: number; replace: number; delete: number }
  drift: number
}
export interface AnsibleEnvTarget { project: string; hosts: number; down: number; last_run?: RunBrief }
export interface EnvView { name: string; status: 'ok' | 'pending' | 'failed' | 'running' | 'drift' | 'unknown'; targets: EnvTarget[]; ansible: AnsibleEnvTarget[] }

// ---- plan review ----
export interface Danger { address: string; action: 'delete' | 'replace'; category: string; message: string }
export interface StateRef { exists: boolean; serial: number; lineage?: string }
export interface PlanReview {
  plan: PlanSummary; no_changes: boolean; status: RunStatus; target: RunTarget
  approvable: boolean; confirm_text: string; danger: Danger[]; stale: string[]
  state?: StateRef; planned_at?: string; plan_ttl_seconds: number; commit?: string; plan_file: string
  checks: { tool: string; status: string; count: number }[]
  approval?: { approved_by: string; approved_at: string; confirm_text: string; acknowledged_danger: boolean }
  apply?: ApplySummary; error?: string
}
export interface AttrDiff { path: string; kind: 'add' | 'remove' | 'change' | 'same'; before?: string; after?: string; forces_replacement?: boolean }
export interface ResourceDiff {
  address: string; type: string; action: PlanChange['action']; reason?: string; replace_paths?: string[]
  changed: AttrDiff[]; unchanged: AttrDiff[]; unchanged_count: number; truncated?: boolean
}

// ---- ansible ----
export interface HostStats { ok: number; changed: number; failures: number; unreachable: number; skipped: number; rescued: number; ignored: number }
export interface HostResult {
  phase: 'check' | 'run'; play: string; task: string; action: string; host: string
  status: 'ok' | 'changed' | 'failed' | 'skipped' | 'unreachable'; changed: boolean; ignored?: boolean
  msg?: string; diff?: string; duration?: number
}
export interface HostReach { host: string; reachable: boolean; latency_ms: number; msg?: string; checked_at: string }
export interface LastPlay { run_id: number; kind: string; status: string; phase: string; ok: number; changed: number; failures: number; unreachable: number }
export interface InvHost { name: string; address: string; groups: string[]; os?: string; reachable?: HostReach; last_play?: LastPlay }
export interface InvGroup { name: string; hosts: string[] | null; children: string[] | null; depth: number; total: number; down: number }
export interface InvScope { project: string; env: string; inventory: string }
export interface InventoryResponse {
  scopes: InvScope[]; scope?: InvScope; hosts?: InvHost[]; groups?: InvGroup[]; files?: string[]; playbooks?: string[]
  approval_required?: boolean; source?: string; error?: string
}
export interface VarSource { file: string; level: string; value: string }
export interface VarRow { name: string; value: string; winner?: VarSource; overridden: VarSource[]; status: 'ok' | 'unknown' | 'role-default'; secret?: boolean }
export interface HostDetail {
  host: string; address: string; groups: string[]; facts?: Record<string, unknown>; facts_gathered_at?: string
  reachable?: HostReach; vars: { rows: VarRow[]; encrypted_files?: string[] }
}

// ---- graph / drift ----
export interface GraphNode {
  id: string; kind: 'resource' | 'data'; type: string; name: string; module?: string; count?: string
  file: string; line: number; end_line: number; action?: string; drift?: boolean; errors?: number
}
export interface GraphEdge { from: string; to: string; label?: string }
export interface GraphModel { nodes: GraphNode[]; edges: GraphEdge[]; source: string; modules: { address: string; source: string; dir: string }[]; errors?: string[] }
export interface ArchContainer { id: string; label: string; children: string[] }
export interface Architecture { nodes: GraphNode[]; edges: GraphEdge[]; containers: ArchContainer[]; unmapped: string[]; errors?: string[] }
export interface DriftAttr { id: number; root: string; env: string; address: string; action: string; attr_path: string; code: string; actual: string; run_id: number; detected_at: string }
export interface DriftResource { address: string; action: 'update' | 'delete'; detected_at: string; run_id: number; attrs: DriftAttr[] }

// ---- variables & secrets ----
export interface VarLoc { file: string; line: number }
export interface VarDecl {
  name: string; type: string; description?: string; default?: string; has_default: boolean; sensitive: boolean
  validation?: string[]; declared: VarLoc; used_in: VarLoc[] | null
}
export interface VarCell {
  root: string; env: string; state: 'set' | 'default' | 'env' | 'secret' | 'missing' | 'absent'
  value: string; source: string; file?: string; line?: number; writable: string[]
}
export interface VarRow { name: string; decl?: VarDecl; cells: VarCell[]; differs: boolean; missing: boolean; unused: boolean; sensitive: boolean }
export interface Stack { id: string; targets: { root: string; env: string }[] }
export interface TerraformVarsResponse { stacks: Stack[]; stack?: string; rows: VarRow[] }

export interface MatrixCell { state: 'set' | 'vault' | 'absent'; value?: string; source?: string; file?: string; line?: number }
export interface MatrixRow { group: string; name: string; secret?: boolean; differs?: boolean; missing?: boolean; cells: Record<string, MatrixCell> }
export interface AnsibleVarsResponse {
  projects: string[]; project?: string
  matrix?: { envs: string[]; rows: MatrixRow[] | null; encrypted_files?: Record<string, string[]> }
}

export interface SecretItem {
  id: string; kind: 'sops' | 'vault-file' | 'vault-inline' | 'env'; name: string; store: string
  file?: string; line?: number; state: string; message?: string; revealable: boolean
}
export interface RevealAudit { id: number; kind: string; name: string; file: string; revealed_at: string }
export interface SecretsResponse { secrets: SecretItem[]; reveals: RevealAudit[] }
export interface SecretFinding { file: string; line: number; col: number; rule: string; severity: string; message: string; key?: string; excerpt: string }

// ---- troubleshooting ----
export interface IssueAction { kind: 'run' | 'open' | 'copy'; label: string; href?: string; text?: string; command?: string; confirm?: string; mutating?: boolean }
export interface IssueStep { title: string; detail?: string; action?: IssueAction }
export interface IssueCheck { ok: boolean; detail: string }
export interface Issue {
  id: number; rule_id: string; category: string; severity: string; title: string; explain: string; target: string
  status: 'open' | 'resolved'; first_seen: string; last_seen: string; resolved_at?: string; resolution?: string
  run_id?: number; file?: string; line?: string; facts?: Record<string, string>; excerpt?: string
  checks?: IssueCheck[]; steps: IssueStep[]
}
export interface IssuesResponse { open: Issue[]; resolved: Issue[]; resolved_this_week: number; rule_errors?: string[] }
export interface DoctorCheck { id: string; name: string; status: 'ok' | 'warn' | 'fail' | 'missing'; detail: string; hint?: string }
export interface DoctorReport { ran_at: string; checks: DoctorCheck[] }
export interface OnboardingItem { id: string; label: string; done: boolean; href: string }
export interface Onboarding { tour_completed: boolean; checklist_hidden: boolean; tips: boolean; items: OnboardingItem[] }
export interface DriftSchedule { schedule?: string; notify?: string; next?: string; last_run?: string; error?: string }

export interface ServiceInfo { version: string; home: string; repos_dir: string }
export interface ProjectView {
  id: string; name: string; path: string; remote?: string; added: string
  status: 'ok' | 'unavailable'; error?: string
  configured?: boolean; config_error?: string; active_runs?: number; open_issues?: number
}
