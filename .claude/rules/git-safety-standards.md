# Git Safety Standards

> **Core Principle**: Every git operation must be recoverable. If an operation could permanently destroy work on remote branches, it is forbidden.

## Command-Level Enforcement

Destructive and forbidden commands are **blocked by deny permissions** in `.claude/settings.json`. This includes:
- Direct pushes to protected branches (main, master, prod, dev, int, release/*)
- Force push (`--force`, `-f`, `--force-with-lease`)
- Destructive resets (`git reset --hard`)
- Bulk discard (`git checkout .`, `git restore .`, `git clean -f`)
- Force branch deletion (`git branch -D`)
- History rewriting (`git rebase`, `git merge --squash`, `gh pr merge --rebase`)

These blocks cannot be overridden. Use safe alternatives: `git stash`, `git reset --soft`, `git revert`, `git branch -d`.

---

## Credential Security (Pre-Commit Requirement)

Before creating ANY file that may contain credentials:
1. **Verify the file pattern is in `.gitignore`** BEFORE creating the file
2. **Refuse to create credential files** if they are not gitignored
3. **Add to `.gitignore` FIRST** if the pattern is missing

### Files That MUST Be Gitignored BEFORE Creation

```
.env / .env.* / *.env          # Environment variables
.secret / *.secret             # Secret files
secrets.* / credentials.*      # Credential configs
*.key / *.pem / *.p12 / *.jks # Keys and certificates
.ocm/config                    # OCM credentials
.docker/config.json            # Docker registry credentials
kubeconfig / kubeconfig.*      # Kubernetes credentials
**/serviceaccount.json         # Service account keys
**/sa-key.json                 # Service account keys
```

### Credential Detection

Scan staged changes before committing:
```shell
git diff --cached | grep -iE '(password|secret|key|token|credential|api_key|apikey|auth|bearer|ghp_|gho_|github_pat)'
```

If credentials were accidentally committed: **rotate the credential immediately**, then clean history with `git filter-branch` or BFG Repo-Cleaner.

---

## Branch Naming Convention

Branch names **MUST** follow semantic patterns:

```shell
feature/<ticket-id>-<short-description>
fix/<ticket-id>-<short-description>
docs/<short-description>
hotfix/<ticket-id>-<short-description>
```

---

## Pull Request Standards

- **Squash merge is the default** for GitHub PRs: `gh pr merge <pr-number> --squash`
- Standard merge is also allowed: `gh pr merge <pr-number> --merge`
- When merging locally (not via PR), use regular merge commits — no squash, no rebase
