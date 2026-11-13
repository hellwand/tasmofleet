// tools/wt.sh creates, lists and removes the worktree of a change (#775). The tests build a temporary
// repository with a bare origin (no network, no GitHub) and run the real script in it. Each test names a way an
// agent loses work or damages the main clone with hand-typed worktree commands: removing a worktree whose commits
// are not on main, removing one with uncommitted files, refusing a branch that is on main only as a cherry-pick
// (changes reach main through cherry-picked merge trains, so the commit ids differ but the patches are the same),
// and touching the main checkout or the remote branch.
import { test, after } from "node:test";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import {
  mkdtempSync,
  mkdirSync,
  writeFileSync,
  chmodSync,
  readFileSync,
  existsSync,
  rmSync,
  realpathSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const script = join(
  dirname(fileURLToPath(import.meta.url)),
  "..",
  "tools",
  "wt.sh",
);
const made = [];
after(() => {
  for (const d of made) rmSync(d, { recursive: true, force: true });
});

// One sandbox per test: <root>/origin.git (bare), <root>/tasmofleet (the main clone, with one commit on main
// pushed to origin) and <root>/other (a second clone that plays "someone else pushes to main"). The git config
// is private to the sandbox, so the tests do not depend on the signing setup of the machine they run on.
function sandbox() {
  const root = realpathSync(mkdtempSync(join(tmpdir(), "wt-test-")));
  made.push(root);
  const cfg = join(root, "gitconfig");
  writeFileSync(
    cfg,
    '[user]\n\tname = Test\n\temail = test@example.invalid\n[commit]\n\tgpgsign = false\n[init]\n\tdefaultBranch = main\n[protocol "file"]\n\tallow = always\n',
  );
  const env = {
    ...process.env,
    GIT_CONFIG_GLOBAL: cfg,
    GIT_CONFIG_NOSYSTEM: "1",
  };
  const git = (cwd, ...args) => {
    const r = spawnSync("git", args, { cwd, env, encoding: "utf8" });
    assert.equal(r.status, 0, `git ${args.join(" ")}: ${r.stderr}`);
    return r.stdout.trim();
  };
  const wt = (cwd, ...args) =>
    spawnSync("bash", [script, ...args], { cwd, env, encoding: "utf8" });
  const origin = join(root, "origin.git");
  const main = join(root, "tasmofleet");
  const other = join(root, "other");
  git(root, "init", "-q", "--bare", origin);
  git(root, "clone", "-q", origin, main);
  writeFileSync(join(main, "README"), "base\n");
  git(main, "add", "README");
  git(main, "commit", "-q", "-m", "base");
  git(main, "push", "-q", "origin", "main");
  git(root, "clone", "-q", origin, other);
  // commit <file>=<content> in <cwd> with the subject <msg>, return the new commit id
  const commit = (cwd, file, content, msg) => {
    writeFileSync(join(cwd, file), content);
    git(cwd, "add", file);
    git(cwd, "commit", "-q", "-m", msg);
    return git(cwd, "rev-parse", "HEAD");
  };
  return { root, main, other, origin, env, git, wt, commit };
}

test("new: a worktree beside the main clone on a new branch from the fetched origin/main", () => {
  // The base must be what origin/main is now, not what the clone last saw: a worktree cut from a stale main
  // makes every later rebase and CI run start from the wrong place.
  const s = sandbox();
  const moved = s.commit(s.other, "later.txt", "x\n", "main moved on");
  s.git(s.other, "push", "-q", "origin", "main");
  const r = s.wt(s.main, "new", "fix-x");
  assert.equal(r.status, 0, r.stderr);
  const path = join(s.root, "tasmofleet-fix-x");
  assert.equal(r.stdout.trim(), path, "prints the path of the worktree");
  assert.equal(s.git(path, "rev-parse", "--abbrev-ref", "HEAD"), "fix-x");
  assert.equal(
    s.git(path, "rev-parse", "HEAD"),
    moved,
    "cut from the fetched origin/main",
  );
  assert.equal(
    spawnSync("git", ["config", "branch.fix-x.merge"], {
      cwd: path,
      env: { ...process.env, GIT_CONFIG_GLOBAL: join(s.root, "gitconfig") },
    }).status,
    1,
    "the branch must not track origin/main: its first push is git push -u origin fix-x",
  );
});

test("new: works from inside another worktree, with an explicit base, and refuses what already exists", () => {
  const s = sandbox();
  const first = join(s.root, "tasmofleet-a");
  assert.equal(s.wt(s.main, "new", "a").status, 0);
  const tip = s.commit(first, "a.txt", "a\n", "work on a");
  // from inside a worktree the new one still lands beside the MAIN clone, based on the given base
  const r = s.wt(first, "new", "b", "a");
  assert.equal(r.status, 0, r.stderr);
  const second = join(s.root, "tasmofleet-b");
  assert.equal(r.stdout.trim(), second);
  assert.equal(s.git(second, "rev-parse", "HEAD"), tip);
  const again = s.wt(s.main, "new", "a");
  assert.equal(again.status, 1, "branch and directory exist");
  assert.match(again.stderr, /already exists/);
  const badBase = s.wt(s.main, "new", "c", "no-such-ref");
  assert.equal(badBase.status, 1);
  assert.equal(existsSync(join(s.root, "tasmofleet-c")), false);
  for (const topic of ["Bad Name", "../x", "a/b", ".hidden", ""]) {
    assert.equal(s.wt(s.main, "new", topic).status, 2, `topic '${topic}'`);
  }
});

test("new leaves the working tree of the main checkout alone", () => {
  // Uncommitted work in the main clone belongs to its owner; creating a worktree beside it must not touch it.
  const s = sandbox();
  writeFileSync(join(s.main, "README"), "edited, not committed\n");
  writeFileSync(join(s.main, "untracked.txt"), "mine\n");
  const before = s.git(s.main, "status", "--porcelain");
  assert.equal(s.wt(s.main, "new", "t").status, 0);
  assert.equal(s.git(s.main, "status", "--porcelain"), before);
  assert.equal(
    readFileSync(join(s.main, "README"), "utf8"),
    "edited, not committed\n",
  );
  assert.equal(s.git(s.main, "rev-parse", "--abbrev-ref", "HEAD"), "main");
});

test("list: shows branch, head and whether the branch is contained in origin/main", () => {
  const s = sandbox();
  s.wt(s.main, "new", "t");
  const path = join(s.root, "tasmofleet-t");
  const line = (out, p) => out.split("\n").find((l) => l.startsWith(p)) ?? "";
  let r = s.wt(s.main, "list");
  assert.equal(r.status, 0, r.stderr);
  assert.match(line(r.stdout, s.main), /\bmain\b.*main checkout/);
  assert.match(line(r.stdout, path), /\bt\b.*contained in origin\/main/);

  const head = s.commit(path, "t.txt", "t\n", "work on t");
  r = s.wt(s.main, "list");
  const l = line(r.stdout, path);
  assert.match(l, /1 commit\(s\) not on origin\/main/);
  assert.ok(l.includes(head.slice(0, 7)), "shows the short head");

  writeFileSync(join(path, "scratch"), "x\n");
  assert.match(line(s.wt(s.main, "list").stdout, path), /uncommitted changes/);
});

test("done refuses a branch with commits that are not on origin/main, and removes nothing", () => {
  // The whole point of the check: work that exists only in this worktree is lost with it.
  const s = sandbox();
  s.wt(s.main, "new", "t");
  const path = join(s.root, "tasmofleet-t");
  s.commit(path, "t.txt", "t\n", "the unmerged work");
  const r = s.wt(s.main, "done", "t");
  assert.equal(r.status, 1);
  assert.match(r.stderr, /the unmerged work/, "names what is missing");
  assert.ok(existsSync(path), "worktree kept");
  assert.ok(s.git(s.main, "branch", "--list", "t"), "branch kept");
});

test("done refuses a dirty tree even when every commit is on main", () => {
  // Contained commits say nothing about files that were never committed.
  const s = sandbox();
  s.wt(s.main, "new", "t");
  const path = join(s.root, "tasmofleet-t");
  s.commit(path, "t.txt", "t\n", "work");
  s.git(path, "push", "-q", "origin", "t:main");
  writeFileSync(join(path, "t.txt"), "edited after the commit\n");
  let r = s.wt(s.main, "done", "t");
  assert.equal(r.status, 1);
  assert.match(r.stderr, /uncommitted or untracked/);
  assert.ok(existsSync(path));

  s.git(path, "checkout", "--", "t.txt");
  writeFileSync(join(path, "new-file.txt"), "never added\n");
  r = s.wt(s.main, "done", "t");
  assert.equal(r.status, 1, "an untracked file counts as dirty too");
  assert.match(r.stderr, /new-file\.txt/);
  assert.ok(existsSync(path));
  assert.ok(s.git(s.main, "branch", "--list", "t"));
});

test("done after a fast-forward merge removes worktree and local branch, not the remote branch", () => {
  const s = sandbox();
  s.wt(s.main, "new", "t");
  const path = join(s.root, "tasmofleet-t");
  s.commit(path, "t.txt", "t\n", "work");
  s.git(path, "push", "-q", "-u", "origin", "t");
  s.git(path, "push", "-q", "origin", "t:main"); // the maintainer's fast-forward push
  const r = s.wt(s.main, "done", "t");
  assert.equal(r.status, 0, r.stderr);
  assert.equal(existsSync(path), false);
  assert.equal(s.git(s.main, "branch", "--list", "t"), "");
  assert.match(
    s.git(s.root, "--git-dir", s.origin, "branch", "--list", "t"),
    /\bt\b/,
    "the remote branch is never deleted",
  );
});

// Lands the commits <shas> of the branch <branch> on origin/main the way a merge train does: someone else's
// change first, then `git cherry-pick` of each commit in a different clone. Same patches, new commit ids.
function cherryPickToMain(s, branch, ...subjects) {
  s.git(s.other, "fetch", "-q", "origin");
  s.commit(s.other, "other.txt", "o\n", "other change");
  const shas = s
    .git(s.other, "rev-list", "--reverse", `origin/main..origin/${branch}`)
    .split("\n");
  for (const sha of shas) {
    const subject = s.git(s.other, "log", "-1", "--format=%s", sha);
    if (subjects.length === 0 || subjects.includes(subject))
      s.git(s.other, "cherry-pick", sha);
  }
  s.git(s.other, "push", "-q", "origin", "main");
}

test("done after a cherry-picked merge: same patch, different commit id, still counts as contained", () => {
  // Merge trains and rebases put the content on main under new commit ids, so the branch tip is not an
  // ancestor of main and `git branch -d` would refuse. The check has to go by patch.
  const s = sandbox();
  s.wt(s.main, "new", "t");
  const path = join(s.root, "tasmofleet-t");
  const mine = s.commit(path, "t.txt", "t\n", "work");
  s.git(path, "push", "-q", "-u", "origin", "t");
  cherryPickToMain(s, "t");
  s.git(s.main, "fetch", "-q", "origin");
  assert.notEqual(
    spawnSync("git", ["merge-base", "--is-ancestor", mine, "origin/main"], {
      cwd: s.main,
    }).status,
    0,
    "precondition: the branch tip is NOT an ancestor of origin/main",
  );
  const r = s.wt(s.main, "done", "t");
  assert.equal(r.status, 0, r.stderr);
  assert.equal(existsSync(path), false);
  assert.equal(s.git(s.main, "branch", "--list", "t"), "");
  assert.match(
    s.git(s.root, "--git-dir", s.origin, "branch", "--list", "t"),
    /\bt\b/,
    "the remote branch is never deleted",
  );
});

test("done refuses when only some commits of the branch were cherry-picked, and names the others", () => {
  // A branch is contained only when EVERY commit is; one landed commit must not release the rest.
  const s = sandbox();
  s.wt(s.main, "new", "t");
  const path = join(s.root, "tasmofleet-t");
  s.commit(path, "one.txt", "1\n", "first, landed");
  s.commit(path, "two.txt", "2\n", "second, not landed");
  s.git(path, "push", "-q", "-u", "origin", "t");
  cherryPickToMain(s, "t", "first, landed");
  const r = s.wt(s.main, "done", "t");
  assert.equal(r.status, 1);
  assert.match(r.stderr, /second, not landed/);
  assert.doesNotMatch(r.stderr, /first, landed/);
  assert.ok(existsSync(path));
});

test("done never removes the main checkout, an unknown topic, or the worktree the shell is in", () => {
  const s = sandbox();
  let r = s.wt(s.main, "done", "main");
  assert.equal(r.status, 1);
  assert.match(r.stderr, /main checkout/);
  assert.ok(existsSync(join(s.main, "README")));
  r = s.wt(s.main, "done", "nope");
  assert.equal(r.status, 1);
  assert.match(r.stderr, /no worktree has the branch/);

  s.wt(s.main, "new", "t");
  const path = join(s.root, "tasmofleet-t");
  mkdirSync(join(path, "sub"));
  r = s.wt(join(path, "sub"), "done", "t");
  assert.equal(r.status, 1, "an empty branch is contained, but cwd is inside");
  assert.match(r.stderr, /inside/);
  assert.ok(existsSync(path));
});

test("done removes a worktree whose branch never got a commit (nothing to lose)", () => {
  const s = sandbox();
  s.wt(s.main, "new", "t");
  const r = s.wt(s.main, "done", "t");
  assert.equal(r.status, 0, r.stderr);
  assert.equal(existsSync(join(s.root, "tasmofleet-t")), false);
});

const realGit = spawnSync("sh", ["-c", "command -v git"], {
  encoding: "utf8",
}).stdout.trim();

// A `git` in front of PATH that runs <before> (a shell snippet, "$@" are git's arguments) and then the real git.
function gitShim(s, before) {
  const dir = join(s.root, "fakebin");
  mkdirSync(dir);
  writeFileSync(
    join(dir, "git"),
    `#!/bin/sh\n${before}\nexec ${realGit} "$@"\n`,
  );
  chmodSync(join(dir, "git"), 0o755);
  const env = { ...s.env, PATH: `${dir}:${process.env.PATH}` };
  return (cwd, ...args) =>
    spawnSync("bash", [script, ...args], { cwd, env, encoding: "utf8" });
}

test("done does not read a failing git cherry as 'everything is contained'", () => {
  // The check is the one thing this script exists for. If git cherry itself fails, its output is empty, and
  // empty means "contained": the branch with unmerged work would be deleted (the tip left only as a dangling
  // commit). A failed check has to refuse, like a failed fetch of the base does.
  const s = sandbox();
  s.wt(s.main, "new", "t");
  const path = join(s.root, "tasmofleet-t");
  s.commit(path, "t.txt", "t\n", "the unmerged work");
  const wt = gitShim(
    s,
    'for a in "$@"; do [ "$a" = cherry ] && exit 128; done',
  );
  let r = wt(s.main, "done", "t");
  assert.equal(r.status, 1, r.stderr);
  assert.match(r.stderr, /containment unknown/);
  assert.ok(existsSync(path), "worktree kept");
  assert.ok(s.git(s.main, "branch", "--list", "t"), "branch kept");
  r = wt(s.main, "list");
  assert.doesNotMatch(
    r.stdout,
    /contained in/,
    "list must not claim containment",
  );
  assert.match(r.stdout, /containment unknown/);
});

test("done keeps the branch when a commit lands in the worktree after the check", () => {
  // Between the containment check and the delete the worktree is still alive: a commit made in that window was
  // never checked. The branch is deleted only at the tip that was checked.
  const s = sandbox();
  s.wt(s.main, "new", "t");
  const path = join(s.root, "tasmofleet-t");
  const late = join(s.root, "late-commit.sh");
  writeFileSync(
    late,
    `cd ${path} && echo late > late.txt && ${realGit} add late.txt && ${realGit} commit -q -m "late work"\n`,
  );
  const wt = gitShim(
    s,
    `for a in "$@"; do [ "$a" = remove ] && (sh ${late}) >/dev/null; done`,
  );
  const r = wt(s.main, "done", "t");
  assert.equal(r.status, 1, r.stderr);
  assert.match(r.stderr, /moved after the check/);
  assert.match(
    s.git(s.main, "log", "-1", "--format=%s", "refs/heads/t"),
    /late work/,
    "the late commit is still reachable from the branch",
  );
});

test("done refuses a merge commit that origin/main does not contain, although git cherry skips merges", () => {
  // git cherry compares patches of non-merge commits only. A merge commit may carry content of its own (a
  // conflict resolution, a file added in the merge), so a branch whose side commits are on main but whose
  // merge commit is not must not be released.
  const s = sandbox();
  s.wt(s.main, "new", "t");
  const path = join(s.root, "tasmofleet-t");
  s.commit(path, "t.txt", "t\n", "work");
  s.git(path, "checkout", "-q", "-b", "side", "main");
  s.commit(path, "side.txt", "s\n", "side work");
  s.git(path, "checkout", "-q", "t");
  writeFileSync(join(path, "only-in-merge.txt"), "m\n");
  s.git(path, "merge", "-q", "--no-ff", "--no-commit", "side");
  s.git(path, "add", "only-in-merge.txt");
  s.git(path, "commit", "-q", "-m", "the merge");
  s.git(path, "push", "-q", "origin", "t");
  cherryPickToMain(s, "t", "work", "side work");
  const r = s.wt(s.main, "done", "t");
  assert.equal(r.status, 1, r.stderr);
  assert.match(r.stderr, /the merge/);
  assert.ok(existsSync(path), "worktree kept");
});

test("usage errors exit 2", () => {
  const s = sandbox();
  for (const args of [[], ["bogus"], ["new"], ["done"], ["list", "x"]]) {
    assert.equal(s.wt(s.main, ...args).status, 2, args.join(" "));
  }
});
