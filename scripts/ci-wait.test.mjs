// tools/ci-wait.sh waits for the checks of a pull request and prints the verdict of the "required" check (#774).
// A fake `gh`, `sleep` and `date` stand in for GitHub and the clock, so the tests need no network and no real
// waiting, and the time limit is tested with the real constants. Each test names a way an agent was misled by a
// hand-written wait (the "required" job not registered yet reads like success, a push during the wait, a wait
// that never ends) and checks that the script reports the truth about the commit it names.
import { test, after } from "node:test";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import {
  mkdtempSync,
  writeFileSync,
  readFileSync,
  existsSync,
  chmodSync,
  rmSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const script = join(
  dirname(fileURLToPath(import.meta.url)),
  "..",
  "tools",
  "ci-wait.sh",
);
const made = [];
after(() => {
  for (const d of made) rmSync(d, { recursive: true, force: true });
});

// Each call of `gh pr view` / `gh pr checks` takes the next line of its queue; the last line repeats forever.
// A checks line "NONE" is what gh prints when no check is registered (a message, no JSON, exit 1); "FAIL" is a
// gh that cannot reach GitHub. `sleep N` only advances the fake clock that `date +%s` reads.
const fakeGh = `#!/bin/sh
echo "$*" >> "$STATE/gh.log"
pop() {
  f=$STATE/$1.q
  n=$(wc -l <"$f")
  line=$(head -n1 "$f")
  if [ "$n" -gt 1 ]; then tail -n +2 "$f" >"$f.t" && mv "$f.t" "$f"; fi
  printf '%s\\n' "$line"
}
[ "$1" = pr ] || { echo "unexpected gh $*" >&2; exit 1; }
case "$2" in
  view) l=$(pop heads) ;;
  checks) l=$(pop checks) ;;
  *) echo "unexpected gh $*" >&2; exit 1 ;;
esac
case "$l" in
  FAIL) echo "error connecting to api.github.com" >&2; exit 1 ;;
  NONE) echo "no checks reported on the 'x' branch" >&2; exit 1 ;;
  *) printf '%s\\n' "$l" ;;
esac
`;
const fakeSleep = `#!/bin/sh
echo "$1" >> "$STATE/sleeps.log"
echo $(( $(cat "$STATE/clock") + $1 )) > "$STATE/clock"
`;
const fakeDate = `#!/bin/sh
[ "$1" = "+%s" ] || { echo "unexpected date $*" >&2; exit 1; }
cat "$STATE/clock"
`;

const job = (name, bucket, workflow = "pr") => ({ name, bucket, workflow });
const other = job("checks / lint", "pass");
const required = (bucket) => job("required", bucket);

// heads / checks: arrays of queue lines (checks: arrays of jobs, or "NONE" / "FAIL").
function run({ heads = ["abc123"], checks, args = ["7"] }) {
  const dir = mkdtempSync(join(tmpdir(), "ci-wait-"));
  made.push(dir);
  for (const [n, body] of [
    ["gh", fakeGh],
    ["sleep", fakeSleep],
    ["date", fakeDate],
  ]) {
    writeFileSync(join(dir, n), body);
    chmodSync(join(dir, n), 0o755);
  }
  const q = (a) =>
    a.map((x) => (typeof x === "string" ? x : JSON.stringify(x))).join("\n") +
    "\n";
  writeFileSync(join(dir, "heads.q"), q(heads));
  writeFileSync(join(dir, "checks.q"), q(checks));
  writeFileSync(join(dir, "clock"), "1000000\n");
  const r = spawnSync("bash", [script, ...args], {
    encoding: "utf8",
    // A script that ignored its limit would loop on the fake clock for ever: fail the test instead of hanging.
    timeout: 20000,
    env: { ...process.env, PATH: `${dir}:${process.env.PATH}`, STATE: dir },
  });
  const file = (n) =>
    existsSync(join(dir, n)) ? readFileSync(join(dir, n), "utf8") : "";
  return {
    ...r,
    sleeps: file("sleeps.log").split("\n").filter(Boolean).map(Number),
    gh: file("gh.log"),
    waited: Number(file("clock")) - 1000000,
  };
}

test("no checks yet, then pending, then green: success only once required is green", () => {
  // "No checks yet" must not read as success (gh pr checks --required --watch does), and neither must a
  // green non-required job while the job "required" is not registered.
  const r = run({
    checks: [
      "NONE",
      "NONE",
      [other],
      [other, required("pending")],
      [other, required("pass")],
    ],
  });
  assert.equal(r.status, 0, r.stderr);
  assert.equal(r.stdout, "required=success sha=abc123\n");
  assert.deepEqual(
    r.sleeps,
    [30, 30, 30, 30],
    "one poll per reading, until required was green",
  );
});

test("the script never uses --watch or --required", () => {
  // --required --watch ends at once while "required" is not registered; --watch alone cannot be bounded by a slice.
  const r = run({ checks: ["NONE", [other, required("pass")]] });
  assert.equal(r.status, 0, r.stderr);
  assert.doesNotMatch(r.gh, /--watch|--required/);
});

test("a red required check is a failure at once, even while other jobs still run", () => {
  const r = run({ checks: [[job("e2e", "pending"), required("fail")]] });
  assert.equal(r.status, 1);
  assert.equal(r.stdout, "required=failure sha=abc123\n");
  assert.deepEqual(r.sleeps, [], "a failure is final: no further waiting");
});

test("a cancelled or skipped required is not a success", () => {
  for (const bucket of ["cancel", "skipping"]) {
    const r = run({ checks: [[other, required(bucket)]] });
    assert.equal(r.status, 1, bucket);
    assert.equal(r.stdout, "required=failure sha=abc123\n", bucket);
  }
});

test("a job called required of another workflow does not count", () => {
  // The same rule as tools/release-green.sh: only pr.yml's job gates the merge.
  const r = run({
    checks: [[job("required", "pass", "other"), other]],
    args: ["7", "1"],
  });
  assert.equal(r.status, 8);
  assert.equal(r.stdout, "required=none sha=abc123\n");
});

test("the time limit ends the wait with pending and exit 8, never with success", () => {
  const r = run({ checks: [[other, required("pending")]], args: ["7", "2"] });
  assert.equal(r.status, 8);
  assert.equal(r.stdout, "required=pending sha=abc123\n");
  assert.equal(r.waited, 120, "waited exactly the 2 minutes, not longer");
});

test("checks that never register end with none at the limit", () => {
  const r = run({ checks: ["NONE"], args: ["7", "1"] });
  assert.equal(r.status, 8);
  assert.equal(r.stdout, "required=none sha=abc123\n");
  assert.equal(r.waited, 60);
});

test("the default limit is one 9 minute slice, so one call fits a tool call of 10 minutes", () => {
  const r = run({ checks: [[required("pending")]] });
  assert.equal(r.status, 8);
  assert.equal(r.waited, 540);
});

test("a longer limit runs in slices of at most 9 minutes and says so on stderr", () => {
  const r = run({ checks: [[required("pending")]], args: ["7", "20"] });
  assert.equal(r.status, 8);
  assert.equal(r.waited, 1200);
  assert.match(r.stderr, /slice 2, 11 minute\(s\) left/);
  assert.match(r.stderr, /slice 3, 2 minute\(s\) left/);
  assert.doesNotMatch(r.stderr, /slice 4/);
  assert.equal(
    r.stdout.trim().split("\n").length,
    1,
    "one result line on stdout",
  );
});

test("a push during the wait is noticed: green checks read for the old head are discarded", () => {
  // Head A, checks green, but the head is B when the reading is done: those checks belong to A. The script
  // must wait for B and name B, never report success for the commit that is no longer the head.
  const r = run({
    heads: ["aaa111", "bbb222", "bbb222", "bbb222"],
    checks: [[required("pass")], [required("pass")]],
  });
  assert.equal(r.status, 0, r.stderr);
  assert.equal(r.stdout, "required=success sha=bbb222\n");
  assert.deepEqual(r.sleeps, [30], "the old head's green was not accepted");
  assert.match(r.stderr, /head of PR 7 moved \(aaa111 -> bbb222\)/);
});

test("a push during the wait that never goes green ends with the new head", () => {
  const r = run({
    heads: ["aaa111", "bbb222"],
    checks: [[required("pending")]],
    args: ["7", "1"],
  });
  assert.equal(r.status, 8);
  assert.equal(r.stdout, "required=pending sha=bbb222\n");
});

test("a head that cannot be read ends with none and sha unknown, not with a guess", () => {
  const r = run({
    heads: ["FAIL"],
    checks: [[required("pass")]],
    args: ["7", "1"],
  });
  assert.equal(r.status, 8);
  assert.equal(r.stdout, "required=none sha=unknown\n");
});

test("usage errors exit 2 and print no result line", () => {
  for (const args of [[], ["x"], ["7", "0"], ["7", "soon"], ["7", "5", "6"]]) {
    const r = run({ checks: ["NONE"], args });
    assert.equal(r.status, 2, JSON.stringify(args));
    assert.equal(r.stdout, "", JSON.stringify(args));
    assert.match(r.stderr, /usage:/);
  }
});
