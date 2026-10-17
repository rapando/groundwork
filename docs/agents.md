# Using groundwork with coding agents

[`skills/groundwork`](../skills/groundwork/SKILL.md) is an [agent skill](https://agentskills.io): instructions that coding agents such as Claude Code load when a task calls for them. It teaches an agent to use the groundwork CLI on your infrastructure repositories:

- validate Terraform and Ansible changes with `groundwork check --json` and fix what it reports;
- diagnose tools, credentials and state backends with `groundwork doctor --json`;
- check for drift, adopt an existing repository with `init`, or scaffold a new one with the right preset and flags;
- edit `groundwork.yaml` without weakening its protections;
- keep secrets out of plain text, and send you to the console to create, edit or rekey ansible-vault values, so a secret never passes through the agent.

## What an agent won't do

groundwork has no command that applies a plan. Plans, applies and playbook runs happen in the console, where you review them. The skill tells the agent to keep it that way:
- it doesn't call the HTTP API with the CLI token;
- it doesn't set `approval: none`;
- it doesn't run `terraform apply` or `ansible-playbook` itself in place of the console.

When the work is ready to deploy, the agent gives you the project's console URL and what to look for in the plan.

## Install the skill

| Agent | Where |
|---|---|
| Claude Code, every repository | `cp -r skills/groundwork ~/.claude/skills/` |
| Claude Code, one repository | `cp -r skills/groundwork <repo>/.claude/skills/` |
| Other skill-aware agents | the agent's skills folder; the skill is a plain `SKILL.md` with two reference files |

The groundwork repository itself links the skill into `.claude/skills/`, so agents working on groundwork have it too.

To pick up a newer version of the skill, copy it again after upgrading groundwork. It documents the CLI of the release it ships with.
