# Design files

UI design for **groundwork** (working name), the local web console for Terraform and Ansible.
The live, clickable canvas is at https://claude.ai/code/artifact/34d1ca5b-00fc-4a98-a3a9-b840d47fe961. This folder is a snapshot of its source.

## What's in here

Each `*.dc.html` file is one screen ("artboard"). They are Design Component sources: plain HTML and inline CSS, a `{{hole}}` template syntax, and a small `class Component` block at the bottom that holds the sample data and, on some screens, interaction state. They need the canvas runtime (`support.js`) to render, so open the canvas link to see them. Read the files for exact colours, spacing, copy and structure.

`canvas.json` holds the board layout: positions, titles and which screens are interactive.

| File | Screen | Notes |
|---|---|---|
| `Main.dc.html` | Overview dashboard (chosen direction, dark) | First-run tour and "Getting started" checklist (`firstRun` prop) |
| `Light.dc.html` | Overview, Option B (light) | Rejected direction, kept for reference |
| `Code.dc.html` | Code explorer + verification | Code / Split / Visualize toggle, IaC-only file filter |
| `CodeVisual.dc.html` | Code screen opened in split view | Embeds `Code.dc.html` with `startView="split"` |
| `Run.dc.html` | Run list + run detail + live logs | Stage pipeline, per-resource apply progress |
| `Plan.dc.html` | Plan review + approval | Replace warning, typed confirmation gate |
| `Graph.dc.html` | Resource dependency graph | Drift / update / create states, code-vs-real diff |
| `Troubleshoot.dc.html` | Issue triage + Doctor checks | Stale state-lock walkthrough |
| `Inventory.dc.html` | Ansible inventory | Hosts, reachability, facts, variable precedence |
| `Variables.dc.html` | Variables matrix + secrets | Per-env values with source, SOPS / ansible-vault |
| `Scaffold.dc.html` | First run in an empty repo | Preset form + file preview |
| `Detect.dc.html` | First run in an existing app repo | Detected IaC roots, env mapping, `groundwork.yaml` preview |

## Design tokens (Option A)

| Token | Value |
|---|---|
| Background | `#0D1012` (sidebar `#0B0E10`, panels `#12161A`, insets `#0A0C0E`) |
| Lines | `#1E2428` (strong `#2A3238`) |
| Text | `#E6E9EB`, muted `#9AA4AB`, labels `#8B959C` |
| Accent | `#B8F36B` (lime), used for primary actions and focus |
| OK / Warn / Fail / Running | `#5FD38D` / `#F5B647` / `#FF7A7A` / `#7CC0FF` |
| Fonts | IBM Plex Sans (UI), JetBrains Mono (code, paths, numbers) |
| Radii | 6px controls, 8–10px cards, 12px large panels |

The app now ships two themes, switchable from the sidebar (or ⌘K → "Switch theme") and remembered per browser: **Paper** (the default: warm off-white `#F6F3EB`, ink text `#1F1C17`, olive accent `#4C7514`) and **Dark** (the tokens above). Both live in `web/src/styles/tokens.css`, including each theme's code-editor syntax palette (`--syn-*`); components use only those variables.

Every status is shown with a text label as well as a colour, so colour is never the only signal.

All data in the designs (hosts, IDs, versions, timings) is made up for the mockups. Values like `[YOUR REGION]` and `[VERSION]` are placeholders.
