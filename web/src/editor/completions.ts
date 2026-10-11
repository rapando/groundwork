import { snippetCompletion as snip, type Completion, type CompletionContext, type CompletionResult, type CompletionSource } from '@codemirror/autocomplete'

// ---------------------------------------------------------------- Terraform

const tfBlocks: Completion[] = [
  snip('resource "${type}" "${name}" {\n\t${}\n}', { label: 'resource', type: 'keyword', detail: 'block' }),
  snip('data "${type}" "${name}" {\n\t${}\n}', { label: 'data', type: 'keyword', detail: 'block' }),
  snip('variable "${name}" {\n\ttype        = ${string}\n\tdescription = "${}"\n}', { label: 'variable', type: 'keyword', detail: 'block' }),
  snip('output "${name}" {\n\tvalue = ${}\n}', { label: 'output', type: 'keyword', detail: 'block' }),
  snip('locals {\n\t${}\n}', { label: 'locals', type: 'keyword', detail: 'block' }),
  snip('module "${name}" {\n\tsource = "${}"\n}', { label: 'module', type: 'keyword', detail: 'block' }),
  snip('provider "${name}" {\n\t${}\n}', { label: 'provider', type: 'keyword', detail: 'block' }),
  snip('terraform {\n\t${}\n}', { label: 'terraform', type: 'keyword', detail: 'block' }),
  snip('moved {\n\tfrom = ${}\n\tto   = ${}\n}', { label: 'moved', type: 'keyword', detail: 'block' }),
  snip('import {\n\tto = ${}\n\tid = "${}"\n}', { label: 'import', type: 'keyword', detail: 'block' }),
  snip('removed {\n\tfrom = ${}\n\tlifecycle {\n\t\tdestroy = ${false}\n\t}\n}', { label: 'removed', type: 'keyword', detail: 'block' }),
  snip('check "${name}" {\n\tassert {\n\t\tcondition     = ${}\n\t\terror_message = "${}"\n\t}\n}', { label: 'check', type: 'keyword', detail: 'block' }),
]

const attr = (label: string, detail?: string): Completion => snip(label + ' = ${}', { label, type: 'property', detail })
const sub = (label: string, detail?: string): Completion => snip(label + ' {\n\t${}\n}', { label, type: 'keyword', detail: detail ?? 'block' })

const tfMeta: Completion[] = [
  attr('count', 'meta-argument'),
  attr('for_each', 'meta-argument'),
  attr('depends_on', 'meta-argument'),
  attr('provider', 'meta-argument'),
  sub('lifecycle', 'meta-argument'),
  snip('dynamic "${name}" {\n\tfor_each = ${}\n\tcontent {\n\t\t${}\n\t}\n}', { label: 'dynamic', type: 'keyword', detail: 'block' }),
  snip('provisioner "${local-exec}" {\n\t${}\n}', { label: 'provisioner', type: 'keyword', detail: 'block' }),
  sub('connection'),
]

/** Arguments valid directly inside a block, by the block's keyword. */
const tfBody: Record<string, Completion[]> = {
  resource: tfMeta,
  data: tfMeta,
  module: [
    attr('source'),
    attr('version'),
    attr('count', 'meta-argument'),
    attr('for_each', 'meta-argument'),
    attr('depends_on', 'meta-argument'),
    attr('providers', 'meta-argument'),
  ],
  variable: [
    attr('type'),
    attr('default'),
    attr('description'),
    attr('sensitive'),
    attr('nullable'),
    attr('ephemeral'),
    snip('validation {\n\tcondition     = ${}\n\terror_message = "${}"\n}', { label: 'validation', type: 'keyword', detail: 'block' }),
  ],
  output: [attr('value'), attr('description'), attr('sensitive'), attr('depends_on'), attr('ephemeral'), sub('precondition')],
  terraform: [
    attr('required_version'),
    sub('required_providers'),
    sub('cloud'),
    snip('backend "${s3}" {\n\t${}\n}', { label: 'backend', type: 'keyword', detail: 'block' }),
    attr('experiments'),
  ],
  lifecycle: [
    attr('create_before_destroy'),
    attr('prevent_destroy'),
    attr('ignore_changes'),
    attr('replace_triggered_by'),
    sub('precondition'),
    sub('postcondition'),
  ],
  precondition: [attr('condition'), attr('error_message')],
  postcondition: [attr('condition'), attr('error_message')],
  validation: [attr('condition'), attr('error_message')],
  assert: [attr('condition'), attr('error_message')],
  moved: [attr('from'), attr('to')],
  import: [attr('to'), attr('id'), attr('identity'), attr('provider'), attr('for_each')],
  provider: [attr('alias'), attr('region')],
}

const tfTypes: Completion[] = [
  ...['string', 'number', 'bool', 'any'].map((label) => ({ label, type: 'type' })),
  ...['list', 'set', 'map', 'object', 'tuple'].map((label) => snip(label + '(${})', { label, type: 'type' })),
]

const tfFunctions = (
  'abs ceil floor log max min parseint pow signum ' +
  'chomp endswith format formatlist indent join lower regex regexall replace split startswith strcontains strrev ' +
  'substr title trim trimprefix trimspace trimsuffix upper ' +
  'alltrue anytrue chunklist coalesce coalescelist compact concat contains distinct element flatten index keys ' +
  'length lookup matchkeys merge one range reverse setintersection setproduct setsubtract setunion slice sort sum ' +
  'transpose values zipmap ' +
  'base64decode base64encode base64gzip csvdecode jsondecode jsonencode textdecodebase64 textencodebase64 ' +
  'urlencode yamldecode yamlencode ' +
  'abspath dirname pathexpand basename file fileexists fileset filebase64 templatefile ' +
  'formatdate plantimestamp timeadd timecmp timestamp ' +
  'base64sha256 base64sha512 bcrypt filebase64sha256 filebase64sha512 filemd5 filesha1 filesha256 filesha512 ' +
  'md5 rsadecrypt sha1 sha256 sha512 uuid uuidv5 ' +
  'cidrhost cidrnetmask cidrsubnet cidrsubnets ' +
  'can ephemeralasnull issensitive nonsensitive sensitive tobool tolist tomap tonumber toset tostring try type'
)
  .split(' ')
  .map((label) => snip(label + '(${})', { label, type: 'function' }))

const tfRoots: Completion[] = [
  { label: 'var', type: 'namespace', detail: 'input variable' },
  { label: 'local', type: 'namespace', detail: 'local value' },
  { label: 'module', type: 'namespace', detail: 'module output' },
  { label: 'data', type: 'namespace', detail: 'data source' },
  { label: 'path', type: 'namespace' },
  { label: 'terraform', type: 'namespace' },
  { label: 'each', type: 'namespace' },
  { label: 'count', type: 'namespace' },
  { label: 'self', type: 'namespace' },
  { label: 'true', type: 'constant' },
  { label: 'false', type: 'constant' },
  { label: 'null', type: 'constant' },
  snip('for ${item} in ${list} : ${expr}', { label: 'for', type: 'keyword', detail: 'expression' }),
]

const tfFixedMembers: Record<string, string[]> = {
  path: ['module', 'root', 'cwd'],
  terraform: ['workspace', 'applying'],
  each: ['key', 'value'],
  count: ['index'],
}

export interface TFSymbols {
  variables: Set<string>
  locals: Set<string>
  modules: Set<string>
  data: Map<string, Set<string>>
  resources: Map<string, Set<string>>
}

/** Names declared across the given HCL sources. */
export function tfSymbols(sources: string[]): TFSymbols {
  const s: TFSymbols = { variables: new Set(), locals: new Set(), modules: new Set(), data: new Map(), resources: new Map() }
  const add = (m: Map<string, Set<string>>, k: string, v: string) => {
    if (!m.has(k)) m.set(k, new Set())
    m.get(k)!.add(v)
  }
  for (const src of sources) {
    for (const m of src.matchAll(/^\s*(variable|module|resource|data)\s+"([^"]+)"(?:\s+"([^"]+)")?/gm)) {
      if (m[1] === 'variable') s.variables.add(m[2])
      else if (m[1] === 'module') s.modules.add(m[2])
      else if (m[3]) add(m[1] === 'data' ? s.data : s.resources, m[2], m[3])
    }
    // keys of locals { ... } blocks: assignments one level inside the block
    for (const m of src.matchAll(/^\s*locals\s*\{/gm)) {
      let depth = 1
      let i = m.index! + m[0].length
      let lineStart: number
      for (; i < src.length && depth > 0; i++) {
        const c = src[i]
        if (c === '{' || c === '[' || c === '(') depth++
        else if (c === '}' || c === ']' || c === ')') depth--
        else if (c === '\n') {
          lineStart = i + 1
          if (depth === 1) {
            const k = /^\s*([A-Za-z_][\w-]*)\s*=/.exec(src.slice(lineStart, lineStart + 200))
            if (k) s.locals.add(k[1])
          }
        }
      }
    }
  }
  return s
}

/**
 * The keywords of the blocks enclosing `pos`, outermost first, ignoring braces
 * in strings and comments. Expression braces (`= {`) are recorded as '='.
 */
export function tfEnclosing(text: string, pos: number): string[] {
  const stack: string[] = []
  let lineStart = 0
  for (let i = 0; i < pos; i++) {
    const c = text[i]
    if (c === '\n') lineStart = i + 1
    else if (c === '"') {
      for (i++; i < pos && text[i] !== '"' && text[i] !== '\n'; i++) if (text[i] === '\\') i++
    } else if (c === '#' || (c === '/' && text[i + 1] === '/')) {
      while (i < pos && text[i] !== '\n') i++
      lineStart = i + 1
    } else if (c === '/' && text[i + 1] === '*') {
      const end = text.indexOf('*/', i + 2)
      i = end < 0 ? pos : end + 1
    } else if (c === '{') {
      const head = text.slice(lineStart, i)
      const kw = /=|\(|\[|:|\?/.test(head) ? '=' : (/^\s*([A-Za-z_][\w-]*)/.exec(head)?.[1] ?? '=')
      stack.push(kw)
    } else if (c === '}') stack.pop()
  }
  return stack
}

function tfMembers(root: string, path: string[], sym: TFSymbols): Completion[] {
  const names = (xs: Iterable<string>, type: string, detail?: string) => [...xs].sort().map((label) => ({ label, type, detail }))
  if (path.length === 0) {
    if (root === 'var') return names(sym.variables, 'variable', 'variable')
    if (root === 'local') return names(sym.locals, 'variable', 'local')
    if (root === 'module') return names(sym.modules, 'variable', 'module')
    if (root === 'data') return names(sym.data.keys(), 'class', 'data source')
    if (tfFixedMembers[root]) return names(tfFixedMembers[root], 'property')
    if (sym.resources.has(root)) return names(sym.resources.get(root)!, 'variable', root)
  }
  if (path.length === 1 && root === 'data' && sym.data.has(path[0])) {
    return names(sym.data.get(path[0])!, 'variable', path[0])
  }
  return []
}

export function terraformCompletions(context: () => string[] = () => []): CompletionSource {
  return (cx: CompletionContext): CompletionResult | null => {
    const doc = cx.state.doc.toString()
    const line = cx.state.doc.lineAt(cx.pos)
    const before = line.text.slice(0, cx.pos - line.from)

    // a reference: var.x, local.x, data.aws_ami.x, aws_instance.web …
    const ref = /([A-Za-z_][\w-]*)((?:\.[A-Za-z_][\w-]*)*)\.([\w-]*)$/.exec(before)
    if (ref) {
      const opts = tfMembers(ref[1], ref[2].split('.').filter(Boolean), tfSymbols([doc, ...context()]))
      return opts.length ? { from: cx.pos - ref[3].length, options: opts, validFor: /^[\w-]*$/ } : null
    }

    const word = cx.matchBefore(/[A-Za-z_][\w-]*/)
    if (!word && !cx.explicit) return null
    const from = word ? word.from : cx.pos
    const lead = before.slice(0, from - line.from)

    // inside a string: only ${…} interpolations get expression completions
    const quotes = (lead.match(/(?<!\\)"/g) ?? []).length
    if (quotes % 2 === 1 && !/\$\{[^}]*$/.test(lead)) return null
    if (/^\s*(#|\/\/)/.test(lead)) return null

    const sym = () => tfSymbols([doc, ...context()])
    const resourceTypes = () => [...sym().resources.keys()].sort().map((label) => ({ label, type: 'class', detail: 'resource' }))

    // the right-hand side of an assignment, a call's arguments or an interpolation
    if (/[=(,[?:]/.test(lead.replace(/"[^"]*"/g, '""')) || /\$\{[^}]*$/.test(lead)) {
      if (/^\s*type\s*=\s*$/.test(lead) || /\b(list|set|map|object|tuple)\(\s*$/.test(lead)) {
        return { from, options: tfTypes, validFor: /^[\w-]*$/ }
      }
      return { from, options: [...tfRoots, ...tfFunctions, ...resourceTypes()], validFor: /^[\w-]*$/ }
    }

    // the start of a line: block keywords at the top level, arguments inside a
    // block; keys of an object expression (`tags = {`) are free-form
    if (!/^\s*$/.test(lead)) return null
    const stack = tfEnclosing(doc, from)
    if (stack.length === 0) return { from, options: tfBlocks, validFor: /^[\w-]*$/ }
    const body = tfBody[stack[stack.length - 1]]
    return body ? { from, options: body, validFor: /^[\w-]*$/ } : null
  }
}

// ---------------------------------------------------------------- Ansible

const ansiblePlay = (
  'hosts name become become_user become_method gather_facts vars vars_files vars_prompt roles tasks pre_tasks ' +
  'post_tasks handlers serial strategy connection remote_user environment tags any_errors_fatal ' +
  'max_fail_percentage order collections module_defaults check_mode diff ignore_unreachable'
).split(' ')

const ansibleTask = (
  'name when loop loop_control register notify tags become become_user ignore_errors changed_when failed_when ' +
  'delegate_to run_once until retries delay environment vars no_log check_mode diff args async poll ' +
  'listen block rescue always throttle timeout local_action with_items with_dict with_fileglob'
).split(' ')

const ansibleIncludes = 'include_tasks import_tasks include_role import_role import_playbook include_vars'.split(' ')

const ansibleModules = (
  'apt apt_key apt_repository assemble assert async_status blockinfile command copy cron debconf debug dnf ' +
  'dpkg_selections expect fail fetch file find gather_facts get_url getent git group group_by hostname ' +
  'iptables known_hosts lineinfile meta package package_facts pause ping pip raw reboot replace rpm_key ' +
  'script service service_facts set_fact set_stats setup shell slurp stat subversion systemd systemd_service ' +
  'sysvinit tempfile template unarchive uri user validate_argument_spec wait_for wait_for_connection yum ' +
  'yum_repository add_host'
).split(' ')

const jinjaVars = (
  'inventory_hostname inventory_hostname_short hostvars groups group_names ansible_facts ansible_host ' +
  'ansible_user ansible_play_hosts ansible_play_batch ansible_check_mode ansible_env playbook_dir role_path ' +
  'inventory_dir item ansible_loop omit'
).split(' ')

const jinjaFilters = (
  'default d bool int float string list dict2items items2dict combine map select reject selectattr rejectattr ' +
  'join split first last length unique sort flatten zip to_json to_nice_json from_json to_yaml to_nice_yaml ' +
  'from_yaml b64encode b64decode basename dirname regex_replace regex_search quote lower upper trim replace ' +
  'password_hash hash mandatory ternary json_query difference intersect union min max sum dict ipaddr'
).split(' ')

export function ansibleCompletions(): CompletionSource {
  const playOpts: Completion[] = ansiblePlay.map((label) => ({ label, type: 'property', detail: 'play' }))
  const taskOpts: Completion[] = [
    ...ansibleTask.map((label) => ({ label, type: 'property', detail: 'task' })),
    ...ansibleIncludes.map((label) => ({ label: 'ansible.builtin.' + label, type: 'keyword', detail: 'include' })),
    ...ansibleModules.map((label) => ({ label: 'ansible.builtin.' + label, type: 'function', detail: 'module' })),
    ...ansibleModules.map((label) => ({ label, type: 'function', detail: 'module', boost: -1 })),
  ]
  const keyOpts = [...new Map([...playOpts, ...taskOpts].map((o) => [o.label, o])).values()]
  const varOpts: Completion[] = jinjaVars.map((label) => ({ label, type: 'variable' }))
  const filterOpts: Completion[] = jinjaFilters.map((label) => ({ label, type: 'function', detail: 'filter' }))

  return (cx: CompletionContext): CompletionResult | null => {
    const line = cx.state.doc.lineAt(cx.pos)
    const before = line.text.slice(0, cx.pos - line.from)

    // Jinja: a filter after `|`, otherwise a variable, inside an open {{ … }}
    const open = before.lastIndexOf('{{')
    if (open >= 0 && before.indexOf('}}', open) < 0) {
      const w = cx.matchBefore(/[\w.]*/)!
      const pre = before.slice(0, w.from - line.from)
      if (/\|\s*$/.test(pre)) return { from: w.from, options: filterOpts, validFor: /^\w*$/ }
      if (!w.text && !cx.explicit) return null
      return { from: w.from, options: varOpts, validFor: /^\w*$/ }
    }

    // a mapping key: at the start of a line, after a `- ` list marker
    const key = /^(\s*)(-\s+)?([\w.]*)$/.exec(before)
    if (!key || (!key[3] && !cx.explicit)) return null
    if (/^\s*#/.test(before)) return null
    // a play is a top-level list item; anything deeper is a task or play body
    const topLevel = key[1].length === 0 && !!key[2]
    return {
      from: cx.pos - key[3].length,
      options: topLevel ? [...playOpts, ...taskOpts.filter((o) => !playOpts.some((p) => p.label === o.label))] : keyOpts,
      validFor: /^[\w.]*$/,
    }
  }
}

/** Ansible YAML: every YAML file except groundwork's own config and CI workflows. */
export function isAnsibleYAML(path: string): boolean {
  return /\.ya?ml$/.test(path) && !/(^|\/)groundwork\.ya?ml$/.test(path) && !/(^|\/)\.github\//.test(path)
}
