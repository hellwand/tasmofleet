#!/usr/bin/env bash
# Create, list and remove the git worktree of a change (#775).
#
#   tools/wt.sh new <topic> [base]   ../<clone>-<topic> on a new branch <topic> from base (default origin/main), after a fetch
#   tools/wt.sh list                 every worktree with its branch, head and whether the branch is contained in origin/main
#   tools/wt.sh done <topic>         remove the worktree and the local branch <topic>, only when it is safe
#
# Run it from the main clone or from any worktree of it. <clone> is the directory name of the main clone
# (tasmofleet), so the worktree of "fix-x" is ../tasmofleet-fix-x.
#
# Why a script: every brief retyped the worktree commands, and "is my branch on main?" was checked by hand
# before a worktree was removed. The check is the part that must not be wrong. Changes reach main through
# fast-forward pushes of rebased branches and through cherry-picked merge trains (docs/dev/pull-requests.md),
# so the commit ids of a branch are often not on main although its content is, and `git branch -d` refuses
# those branches. `done` therefore compares by patch (`git cherry`): every commit of the branch must have
# its patch on origin/main. It also refuses a worktree with uncommitted or untracked files, one the shell is
# standing in, and the main checkout itself. If git cherry fails, containment is unknown and nothing is removed.
#
# What it never does: touch the working tree of the main checkout (a worktree is added or removed beside it,
# the branch is deleted by name, at the tip that was checked), delete a remote branch (no push at all), or
# force anything (no --force on `git worktree remove`; a locked worktree stays). `new` and `done` fetch origin first; for `done` and `list`
# a failed fetch only costs accuracy in the safe direction (a branch looks less merged), so it is a warning.
#
# Exit status: 0 done, 1 refused or failed (the reason is on stderr), 2 usage error.
set -u

MAIN_REF=origin/main
FETCH_LIMIT=120 # seconds: a hung fetch must not hang the agent

usage() {
	echo "usage: tools/wt.sh new <topic> [base] | list | done <topic>" >&2
	exit 2
}
die() {
	echo "wt: $*" >&2
	exit 1
}

# The main checkout is the first entry of `git worktree list`, wherever the script runs.
main=$(git worktree list --porcelain 2>/dev/null | sed -n '1s/^worktree //p')
[ -n "$main" ] || die "not inside a git repository"

fetch() {
	if command -v timeout >/dev/null 2>&1; then
		timeout "$FETCH_LIMIT" git -C "$main" fetch -q origin
	else
		git -C "$main" fetch -q origin
	fi
}

# Branch names a topic may have: one path segment, so that the directory name is predictable.
valid_topic() {
	[[ $1 =~ ^[a-z0-9][a-z0-9._-]*$ ]] && git check-ref-format --branch "$1" >/dev/null 2>&1
}

# Commits of $1 (a commit id) that are not on origin/main, one "<short sha> <subject>" per line.
# git cherry marks a commit "-" when a commit with the same patch id is on origin/main (rebase, cherry-pick),
# "+" when none is. Empty output: everything is contained (also for a branch without commits of its own).
# git cherry skips merge commits, so a merge commit that origin/main does not contain is listed as missing too:
# its own content (a conflict resolution, a file added in the merge) has no patch to compare.
# Returns non-zero when git fails: the output is then not an answer, and callers must not read "empty" as
# "contained" (no pipe here on purpose: the status of git cherry is the status of the function).
missing_commits() {
	local out merges
	out=$(git -C "$main" cherry -v "$MAIN_REF" "$1") || return 1
	merges=$(git -C "$main" log --merges --format='%h %s (merge commit)' "$MAIN_REF..$1") || return 1
	{
		sed -n 's/^+ \([0-9a-f]\{7\}\)[0-9a-f]* /\1 /p' <<<"$out"
		[ -z "$merges" ] || printf '%s\n' "$merges"
	}
}

cmd_new() {
	local topic=${1:-} base=${2:-$MAIN_REF}
	[ $# -ge 1 ] && [ $# -le 2 ] || usage
	valid_topic "$topic" || {
		echo "wt: invalid topic '$topic' (lower case letters, digits, . _ -; a valid branch name)" >&2
		exit 2
	}
	local path
	path="$(dirname "$main")/$(basename "$main")-$topic"
	[ ! -e "$path" ] || die "$path already exists"
	git -C "$main" show-ref --verify --quiet "refs/heads/$topic" && die "branch $topic already exists"
	fetch || die "git fetch origin failed: the base would be stale"
	git -C "$main" rev-parse --verify --quiet "$base^{commit}" >/dev/null || die "base '$base' is not a commit"
	# --no-track: the new branch must not track origin/main (the first push is `git push -u origin <topic>`).
	git -C "$main" worktree add --no-track -b "$topic" "$path" "$base" >&2 || die "git worktree add failed"
	echo "$path"
}

cmd_list() {
	[ $# -eq 0 ] || usage
	git -C "$main" rev-parse --verify --quiet "$MAIN_REF^{commit}" >/dev/null || die "$MAIN_REF not found (no origin/main in this clone)"
	fetch || echo "wt: warning: git fetch origin failed, $MAIN_REF may be stale" >&2
	local path branch head state dirty
	while IFS=$'\t' read -r path branch head; do
		if [ "$path" = "$main" ]; then
			printf '%-44s %-28s %-9s %s\n' "$path" "${branch:-(detached)}" "${head:0:7}" "main checkout"
			continue
		fi
		if [ ! -d "$path" ]; then
			printf '%-44s %-28s %-9s %s\n' "$path" "${branch:-(detached)}" "${head:0:7}" "missing (git worktree prune)"
			continue
		fi
		local missing
		if ! missing=$(missing_commits "$head"); then
			state="containment unknown (git cherry failed)"
		elif [ -z "$missing" ]; then
			state="contained in $MAIN_REF"
		else
			state="$(wc -l <<<"$missing" | tr -d ' ') commit(s) not on $MAIN_REF"
		fi
		dirty=
		[ -z "$(git -C "$path" status --porcelain 2>/dev/null)" ] || dirty=", uncommitted changes"
		printf '%-44s %-28s %-9s %s\n' "$path" "${branch:-(detached)}" "${head:0:7}" "$state$dirty"
	done < <(git -C "$main" worktree list --porcelain | awk -v RS= -F'\n' '{
		path = ""; branch = ""; head = ""
		for (i = 1; i <= NF; i++) {
			if ($i ~ /^worktree /) path = substr($i, 10)
			else if ($i ~ /^HEAD /) head = substr($i, 6)
			else if ($i ~ /^branch refs\/heads\//) branch = substr($i, 19)
		}
		printf "%s\t%s\t%s\n", path, branch, head
	}')
}

cmd_done() {
	local topic=${1:-}
	[ $# -eq 1 ] || usage
	valid_topic "$topic" || {
		echo "wt: invalid topic '$topic'" >&2
		exit 2
	}
	# The worktree is found by its branch, not by a guessed path.
	local path
	path=$(git -C "$main" worktree list --porcelain | awk -v RS= -F'\n' -v b="branch refs/heads/$topic" '
		{ for (i = 1; i <= NF; i++) if ($i == b) { print substr($1, 10); exit } }')
	[ -n "$path" ] || die "no worktree has the branch $topic (see: tools/wt.sh list)"
	[ "$path" != "$main" ] || die "$topic is checked out in the main checkout, which is never removed"
	[ -d "$path" ] || die "$path is missing; run 'git worktree prune', then delete the branch by hand if it is merged"
	# Physical paths on both sides: a symlinked cwd or worktree path must not hide that the shell is inside.
	local here real
	here=$(pwd -P) || die "cannot read the current directory"
	real=$(cd "$path" && pwd -P) || die "cannot resolve $path"
	case "$here/" in "$real"/*) die "the shell is inside $path; cd out of it first" ;; esac

	fetch || echo "wt: warning: git fetch origin failed, $MAIN_REF may be stale (a stale ref only refuses more)" >&2
	git -C "$main" rev-parse --verify --quiet "$MAIN_REF^{commit}" >/dev/null || die "$MAIN_REF not found (no origin/main in this clone)"

	local status
	status=$(git -C "$path" status --porcelain) || die "cannot read the status of $path"
	if [ -n "$status" ]; then
		echo "wt: refusing: $path has uncommitted or untracked files:" >&2
		echo "$status" | sed 's/^/  /' >&2
		exit 1
	fi

	# One full sha for the check and for the delete: a commit made in the worktree after the check moves the
	# branch, and update-ref below then refuses instead of deleting a commit nobody looked at.
	local tip missing
	tip=$(git -C "$main" rev-parse --verify "refs/heads/$topic") || die "cannot resolve the branch $topic"
	missing=$(missing_commits "$tip") || die "git cherry failed in $path: containment unknown, nothing removed"
	if [ -n "$missing" ]; then
		echo "wt: refusing: these commits of $topic are not on $MAIN_REF (not even as an equal patch):" >&2
		echo "$missing" | sed 's/^/  /' >&2
		echo "wt: merge or push them first; $path and the branch are untouched" >&2
		case $missing in *"(merge commit)"*) echo "wt: a merge commit has no patch to compare: rebase the branch onto $MAIN_REF, or delete it by hand after checking the diff" >&2 ;; esac
		exit 1
	fi

	# No --force: git itself refuses a locked worktree or one with files we did not see.
	git -C "$main" worktree remove "$path" || die "git worktree remove failed; the branch is untouched"
	# Not `branch -d` (it wants the tip reachable from HEAD, which a cherry-picked merge never is) and not
	# `branch -D` (it deletes whatever the branch points to now): update-ref deletes only the checked tip.
	git -C "$main" update-ref -d "refs/heads/$topic" "$tip" || die "worktree removed, but the branch $topic moved after the check and was kept (tip checked: $tip)"
	# What `branch -D` also does: drop the branch.<topic>.* config (an upstream set by `git push -u`).
	git -C "$main" config --remove-section "branch.$topic" 2>/dev/null || true
	echo "wt: removed $path and the local branch $topic (it was at ${tip:0:7})"
	if git -C "$main" show-ref --verify --quiet "refs/remotes/origin/$topic"; then
		echo "wt: origin/$topic is left alone (this script never deletes a remote branch)"
	fi
}

[ $# -ge 1 ] || usage
cmd=$1
shift
case $cmd in
new) cmd_new "$@" ;;
list) cmd_list "$@" ;;
done) cmd_done "$@" ;;
*) usage ;;
esac
