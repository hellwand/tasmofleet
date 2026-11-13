# Pull requests, the protected main, branches and worktrees

Part of the working guide: start at [CLAUDE.md](../../CLAUDE.md).

## Pull requests and the protected main

`main` is protected on GitHub by two rulesets. The ruleset `main`: no force pushes, no deletion,
linear history, signed commits, PR and the status check `required` from `pr.yml`; repository
admins bypass it. The ruleset `main: CI must be green (no bypass)` (#709) holds only the status
check `required` (strict) and has no bypass actors, so it applies to admins too. Every change,
docs-only ones included, goes through a pull request: branch, push, open a PR against `main`,
wait until CI is green on the PR, then the maintainer merges it **fast-forward only** by
pushing the PR head to `main`, so the commits keep their own signatures:

```sh
git fetch origin && git rebase origin/main   # if main moved: rebase, re-sign, push, wait for CI again
git push origin <branch>:main                # ff push of the green PR head; GitHub marks the PR merged
```

Only repository admins may push to `main` (bypass of the ruleset `main`), and only a commit whose
`required` check is green: the second ruleset makes GitHub refuse the push (`GH013`)
until `required` has passed on exactly the pushed commit. In a merge train
that is the train's tip, the PR head that ran CI; the release commit rides on it, so releases need
nothing extra. If `main` moved, the branch is rebased (and re-signed), pushed and must be green
again before the fast-forward push; there is no other merge path. A repair that cannot get a green
`required` first (an emergency force push) needs a repository admin to set the second ruleset to disabled deliberately
and back to active right after. Never merge through the GitHub
button: the only method enabled there is rebase, and GitHub's rebase commits are unsigned, so
the ruleset rejects them.
`pr.yml` runs the FAST tier on every PR: a `changes` job detects docs-only PRs (heavy jobs
skipped), PRs that touch a `SAFETY` package (race and diff coverage added) or Go code (revert check added), and the final `required`
job passes only when every job that had to run passed; a skip counts only when the PR did
not ask for that job; the link check, the migration guard and the release section guard must always succeed, and so must the image build check when the PR touches what the image reads (Dockerfile, module files, the HA app folder); the HA app image is built next to it (`_ha-app.yml`) and never gates the merge. No approvals are required (single maintainer). Release tags are still pushed
directly, on a green commit of `main`.

**Waiting for CI:** `tools/ci-wait.sh <pr> [max-minutes]` (#774). Never `gh pr checks --required --watch`: it ends at
once, and reads like success, while the job `required` is not registered yet. The script first waits until checks are
registered, then polls in slices of at most 9 minutes (default limit: 9, one slice, which fits one tool call (run it with the tool timeout at 10 minutes, or in the background); a longer
limit belongs in a background run) and prints one line on stdout, `required=<success|failure|pending|none> sha=<head>`.
Exit 0 means only: `required` of `pr.yml` is green on that head. Failure exits 1; `pending` or `none` at the limit exit 8:
run it again. It re-reads the head of the PR around every reading, so a push during the wait is not mistaken for the
result of the old head; compare `sha=` with the commit you meant to check.

## Branches and worktrees

After a pull request is merged into `main`, its branch is deleted on origin and the
local worktree and branch are removed. Before deleting, check containment:
`git cherry origin/main <branch>` shows only `-` lines, or the branch's diff against
`main` is empty (rebased or squashed work). Branches with uncontained changes are never
deleted silently.

**The script:** `tools/wt.sh` (#775) does this, so it is not retyped in every brief. `tools/wt.sh new <topic> [base]` fetches and creates
`../tasmofleet-<topic>` on a new branch `<topic>` from `origin/main` (or `base`), without upstream; `tools/wt.sh list` shows every worktree with
branch, head and whether its branch is contained in `origin/main`; `tools/wt.sh done <topic>` removes the worktree and the local branch only when
the tree is clean (untracked files count) and every commit of the branch has its patch on `origin/main` (`git cherry`, so a rebase or a
cherry-picked merge train counts although the commit ids differ, where `git branch -d` refuses). Otherwise it lists what is missing and removes
nothing; if `git cherry` itself fails, containment is unknown and it removes nothing. A merge commit that `origin/main` does not contain counts as
missing (`git cherry` compares only non-merge commits, so a conflict resolution inside a merge would otherwise go unseen). The branch is deleted only
at the tip that was checked. Ignored files in the worktree (`data/`, `*.db`, `.env`) are deleted with it.
Run it from the main clone or any worktree, but not from inside the worktree being removed. It never touches the working tree of the main
checkout and never deletes a remote branch (that stays with the maintainer's merge). A squash merge whose result is not a patch of the branch is
not recognised: check it by hand (the diff of the branch against `origin/main` is empty).
