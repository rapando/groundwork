# Rule fixtures

One fixture per rule in `../rules/*.yaml`; `rules_test.go` checks each rule fires on its fixture and that every rule has one.

**Captured** from real tools (terraform 1.16.5, ansible-core 2.21, yamllint), with the user name, host name and paths replaced:
`tf-*` (all), `ans-unreachable-*`, `ans-vault-*`, `ans-become-password`, `ans-python-interpreter` (the warning line, inside a written play), `diags/yamllint-indentation.txt`. The ansible-lint findings come from `testdata/checks/ansible_lint.json`, also captured.

**Written** from the tools' documented messages, because reproducing them needs cloud accounts or a rebuilt SSH host:
`aws-sso-expired`, `aws-no-credentials`, `tf-backend-access-denied`, `ans-host-key-changed`, `ans-ssh-auth-failed`. If you hit one of these for real, replace the fixture with the captured text.
