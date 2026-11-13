#!/usr/bin/env bash
# Wait for the checks of a pull request and print the verdict of the "required" check (#774).
#
#   tools/ci-wait.sh <pr> [max-minutes]       (default 9; env GH_REPO selects another repository, as for gh)
#
# Why a script: `gh pr checks <n> --required --watch` ends at once, and reads like success, while the job
# "required" is not registered yet (docs/pitfalls.md), and one long watch is cut off by the 10 minute limit of a
# tool call. So the script
#   1. waits until checks are registered for the head commit (none at all is "none", not "success"),
#   2. polls in slices of at most 9 minutes (a progress line on stderr per slice) until the check "required" of
#      pr.yml has finished or max-minutes are over; the default of 9 minutes is one slice and fits one tool call,
#      a longer limit belongs in a background run,
#   3. re-reads the head commit of the PR around every reading of the checks: a push during the wait voids what
#      was read for the old head and the wait starts over for the new one (the limit keeps running).
# It never uses --watch or --required, and it never guesses: only a finished, green "required" of pr.yml on the
# head that is still the head is "success".
#
# stdout: exactly one line, required=<success|failure|pending|none> sha=<head>. All progress goes to stderr.
#   success  "required" is green                        exit 0 (the only exit 0)
#   failure  "required" failed, was cancelled or skipped exit 1
#   pending  "required" is registered, not finished     exit 8 (like gh pr checks): run it again
#   none     no "required" registered yet               exit 8
# Usage errors exit 2 and print no result line. sha is "unknown" when the head could not be read at all.
set -u

POLL=30   # seconds between two readings
SLICE=540 # seconds: at most 9 minutes per slice
CALL=20   # seconds: no single gh call may hang longer (three calls per reading: at most 60 s over the limit)

usage() {
	echo "usage: tools/ci-wait.sh <pr-number> [max-minutes]" >&2
	exit 2
}
pr=${1:-}
max=${2:-9}
[[ $pr =~ ^[0-9]+$ ]] || usage
[[ $max =~ ^[0-9]+$ && $max -gt 0 ]] || usage
[ $# -le 2 ] || usage
command -v jq >/dev/null || { echo 'ci-wait: jq is required' >&2; exit 2; }

# gh with a time limit per call (a hung call would break the bound of the whole wait).
ghq() {
	if command -v timeout >/dev/null 2>&1; then timeout "$CALL" gh "$@"; else gh "$@"; fi
}
head_of() { ghq pr view "$pr" --json headRefOid --jq .headRefOid 2>/dev/null; }

# The result for one reading of the checks (JSON from gh pr checks --json name,bucket,workflow).
# Only the job "required" of the workflow "pr" counts; a job of that name in another workflow does not (the same
# rule as tools/release-green.sh). Several of them (a re-run) count as the worst one.
verdict() {
	jq -r '[.[] | select(.name == "required" and .workflow == "pr") | .bucket] as $b
		| if ($b | length) == 0 then "none"
		  elif any($b[]; . == "fail" or . == "cancel" or . == "skipping") then "failure"
		  elif any($b[]; . == "pending") then "pending"
		  elif all($b[]; . == "pass") then "success"
		  else "pending" end' <<<"$1"
}

start=$(date +%s)
deadline=$((start + max * 60))
slice_start=$start
slice=1
sha=unknown
result=none
last=
while :; do
	before=$(head_of) || before=
	if [ -z "$before" ]; then
		result=none
		echo "ci-wait: cannot read the head of PR $pr yet" >&2
	else
		# gh pr checks exits 1 (a failed check) or 8 (pending) with JSON on stdout; without any check it
		# prints a message and no JSON. Whether the output is JSON decides, not the exit status.
		out=$(ghq pr checks "$pr" --json name,bucket,workflow 2>/dev/null)
		after=$(head_of) || after=
		if [ "$after" != "$before" ]; then
			echo "ci-wait: the head of PR $pr moved (${before:0:12} -> ${after:0:12}), starting over for the new head" >&2
			sha=${after:-$before}
			result=none
			last=
		elif jq -e 'type == "array"' <<<"$out" >/dev/null 2>&1; then
			sha=$before
			result=$(verdict "$out")
		else
			sha=$before
			result=none
		fi
	fi
	case $result in success | failure) break ;; esac
	if [ "$result" != "$last" ]; then
		echo "ci-wait: PR $pr ${sha:0:12}: required is $result" >&2
		last=$result
	fi
	now=$(date +%s)
	[ "$now" -lt "$deadline" ] || break
	if [ $((now - slice_start)) -ge $SLICE ]; then
		slice=$((slice + 1))
		slice_start=$now
		echo "ci-wait: slice $slice, $(((deadline - now + 59) / 60)) minute(s) left, required is $result" >&2
	fi
	left=$((deadline - now))
	sleep $((POLL < left ? POLL : left))
done

echo "required=$result sha=$sha"
case $result in
success) exit 0 ;;
failure) exit 1 ;;
*) exit 8 ;;
esac
