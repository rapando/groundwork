import { describe, expect, it } from 'vitest'
import { CompletionContext, type CompletionResult, type CompletionSource } from '@codemirror/autocomplete'
import { EditorState } from '@codemirror/state'
import { indentUnit } from '@codemirror/language'
import { ansibleCompletions, isAnsibleYAML, terraformCompletions, tfEnclosing, tfSymbols } from './completions'
import { guideLevels } from './indentGuides'

/** Run a source with the cursor at the last `|` in `text`. */
function complete(source: CompletionSource, text: string, explicit = false): string[] {
  const pos = text.lastIndexOf('|')
  const state = EditorState.create({ doc: text.slice(0, pos) + text.slice(pos + 1) })
  const r = source(new CompletionContext(state, pos, explicit)) as CompletionResult | null
  return r ? r.options.map((o) => o.label) : []
}

const variables = `
variable "region" {}
variable "env" {
  type = string
}
locals {
  name   = "web"
  tags = {
    Owner = "ops"
  }
}
module "vpc" { source = "./vpc" }
resource "aws_instance" "web" {}
data "aws_ami" "ubuntu" {}
`

describe('tfSymbols', () => {
  it('collects declared names', () => {
    const s = tfSymbols([variables])
    expect([...s.variables]).toEqual(['region', 'env'])
    expect([...s.locals]).toEqual(['name', 'tags'])
    expect([...s.modules]).toEqual(['vpc'])
    expect([...s.resources.get('aws_instance')!]).toEqual(['web'])
    expect([...s.data.get('aws_ami')!]).toEqual(['ubuntu'])
  })
})

describe('tfEnclosing', () => {
  it('tracks blocks and ignores braces in strings and comments', () => {
    const text = 'resource "a" "b" {\n  # {\n  x = "}{"\n  tags = {\n'
    expect(tfEnclosing(text, text.length)).toEqual(['resource', '='])
  })
})

describe('terraformCompletions', () => {
  const src = terraformCompletions(() => [variables])

  it('offers block keywords at the top level', () => {
    expect(complete(src, 'res|')).toContain('resource')
  })
  it('offers arguments of the enclosing block', () => {
    const got = complete(src, 'variable "x" {\n  de|\n}')
    expect(got).toContain('default')
    expect(got).toContain('validation')
    expect(complete(src, 'resource "a" "b" {\n  lifecycle {\n    pre|\n  }\n}')).toContain('prevent_destroy')
  })
  it('completes references from sibling files', () => {
    expect(complete(src, 'output "o" { value = var.|')).toEqual(['env', 'region'])
    expect(complete(src, 'x = local.n|')).toEqual(['name', 'tags'])
    expect(complete(src, 'x = data.aws_ami.|')).toEqual(['ubuntu'])
    expect(complete(src, 'x = aws_instance.|')).toEqual(['web'])
    expect(complete(src, 'x = each.|')).toEqual(['key', 'value'])
  })
  it('offers functions and types in expressions', () => {
    expect(complete(src, 'x = jsonen|')).toContain('jsonencode')
    expect(complete(src, 'variable "v" {\n  type = li|')).toContain('list')
    expect(complete(src, 'x = "${up|')).toContain('upper')
  })
  it('stays quiet inside plain strings, comments and object keys', () => {
    expect(complete(src, 'x = "hel|')).toEqual([])
    expect(complete(src, '# res|')).toEqual([])
    expect(complete(src, 'resource "a" "b" {\n  tags = {\n    Na|')).toEqual([])
  })
})

describe('ansibleCompletions', () => {
  const src = ansibleCompletions()
  it('offers play keys for a top-level item and task keys and modules below', () => {
    expect(complete(src, '- hos|')).toContain('hosts')
    const task = complete(src, '- hosts: all\n  tasks:\n    - ansible.builtin.co|')
    expect(task).toContain('ansible.builtin.copy')
    expect(complete(src, '    - name: x\n      wh|')).toContain('when')
  })
  it('offers variables and filters inside {{ }}', () => {
    expect(complete(src, 'msg: "{{ inventory|')).toContain('inventory_hostname')
    expect(complete(src, 'msg: "{{ x | def|')).toContain('default')
  })
  it('stays quiet in values', () => {
    expect(complete(src, 'name: inst|')).toEqual([])
  })
  it('applies to playbooks but not to groundwork or CI config', () => {
    expect(isAnsibleYAML('ansible/site.yml')).toBe(true)
    expect(isAnsibleYAML('groundwork.yaml')).toBe(false)
    expect(isAnsibleYAML('.github/workflows/ci.yml')).toBe(false)
  })
})

describe('guideLevels', () => {
  it('counts indent levels and carries them through blank lines', () => {
    const state = EditorState.create({
      doc: 'a {\n  b {\n    c\n\n    d\n  }\n}',
      extensions: indentUnit.of('  '),
    })
    expect(guideLevels(state, 1, 7)).toEqual([0, 1, 2, 2, 2, 1, 0])
  })
})
