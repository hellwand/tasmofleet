import { test } from "node:test";
import assert from "node:assert/strict";
import { parseKey, selectSuperseded } from "./prune-go-caches.mjs";

const SUM_A = "a".repeat(64);
const SUM_B = "b".repeat(64);
const sha = (c) => c.repeat(40);

// One cache entry as `gh cache list --json id,key,ref,createdAt,sizeInBytes` prints it.
let nextId = 1;
const entry = ({ name, os = "Linux", go = "go1.27.4", sum = SUM_A, commit = "1", at, ref = "refs/heads/main" }) => ({
  id: nextId++,
  key: `go-${name}-${os}-${go}-${sum}-${sha(commit)}`,
  ref,
  createdAt: at,
  sizeInBytes: 1000,
});
const ids = (xs) => xs.map((x) => x.id).sort((a, b) => a - b);

// Why (#692): the Go version is part of the key, and the floating `go 1.27` moved from 1.27.3 to
// 1.27.4. The 1.27.3 series can then never be restored again but stays in the 10 GB, as large as
// the live one. A series is a name, not a name plus version, so the older Go version goes.
test("a go1.27.3 and a go1.27.4 entry of one name collapse to the newer", () => {
  const old = entry({ name: "test", go: "go1.27.3", at: "2026-11-06T21:01:00Z" });
  const cur = entry({ name: "test", go: "go1.27.4", at: "2026-11-12T03:21:00Z" });
  assert.deepEqual(ids(selectSuperseded([cur, old])), [old.id]);
  assert.deepEqual(ids(selectSuperseded([old, cur])), [old.id]);
});

// Why: every run on main writes a new key (the commit is in it); nothing but this prune removes the
// previous generations, and each one is as large as the cache itself.
test("older generations of one go.sum and Go version go, the newest stays", () => {
  const gens = [1, 2, 3, 4].map((n) => entry({ name: "lint", commit: String(n), at: `2026-11-0${n}T10:00:00Z` }));
  assert.deepEqual(ids(selectSuperseded(gens)), ids(gens.slice(0, 3)));
});

// Why: a changed go.sum starts a new series under the same name. The old one is not restorable
// (restore-keys carry the hash), so the newest entry wins whatever its hash is.
test("a newer entry with another go.sum hash replaces the older hash's entry", () => {
  const old = entry({ name: "test", sum: SUM_A, at: "2026-11-01T10:00:00Z" });
  const cur = entry({ name: "test", sum: SUM_B, at: "2026-11-02T10:00:00Z" });
  assert.deepEqual(ids(selectSuperseded([old, cur])), [old.id]);
});

// Why: the newest wins even when it belongs to the older Go version (a re-run of an old commit
// must not leave the repository without any entry for the name).
test("the newest entry stays whatever Go version it has", () => {
  const a = entry({ name: "test", go: "go1.27.4", at: "2026-11-12T03:21:00Z" });
  const b = entry({ name: "test", go: "go1.27.3", at: "2026-11-13T03:21:00Z" });
  assert.deepEqual(ids(selectSuperseded([a, b])), [a.id]);
});

// Why: go-race-0 and go-race-1 are the two shards of one job kind; a name that only differs in a
// suffix is a different cache, and pruning one by the other would empty a shard's cache every night.
test("go-race-0 and go-race-1 stay apart", () => {
  const r0 = entry({ name: "race-0", at: "2026-11-01T10:00:00Z" });
  const r1 = entry({ name: "race-1", at: "2026-11-02T10:00:00Z" });
  assert.deepEqual(selectSuperseded([r0, r1]), []);
  const r0new = entry({ name: "race-0", commit: "2", at: "2026-11-03T10:00:00Z" });
  assert.deepEqual(ids(selectSuperseded([r0, r1, r0new])), [r0.id]);
});

// Why: names that merely start alike (test, test-x) are two caches.
test("a name that is a prefix of another name is its own series", () => {
  const a = entry({ name: "test", at: "2026-11-01T10:00:00Z" });
  const b = entry({ name: "test-x", at: "2026-11-02T10:00:00Z" });
  assert.deepEqual(selectSuperseded([a, b]), []);
});

// Why: only main's caches are restored by the PR and tag jobs. A cache of a PR ref is not ours to
// judge (it cannot be restored by anything else and GitHub drops it with the PR), and it must not
// make main's only entry look superseded.
test("only refs/heads/main is pruned, a newer entry of another ref does not supersede main's", () => {
  const onMain = entry({ name: "test", at: "2026-11-01T10:00:00Z" });
  const onPr = entry({ name: "test", commit: "2", at: "2026-11-05T10:00:00Z", ref: "refs/pull/9/merge" });
  const onPr2 = entry({ name: "test", commit: "3", at: "2026-11-04T10:00:00Z", ref: "refs/pull/9/merge" });
  assert.deepEqual(selectSuperseded([onMain, onPr, onPr2]), []);
});

// Why: the repository holds buildkit blobs, CodeQL databases, npm and Tasmota-source caches too.
// Those are not Go caches and keep their own rules; a key that only looks like one is left alone.
test("keys that are not Go caches of the composite action are never selected", () => {
  const other = [
    "buildkit-blob-1-sha256:abc",
    `npm-${SUM_A}`,
    "codeql-dependencies-1-Linux-go-ee3af379",
    `codeql-overlay-base-database-1-433570c23e836bda-go-2.27.4-${sha("1")}-43325235563`,
    "ghcr-manifest-v1-hellwand-tasmofleet-43319364718",
    "go-test-Linux-garbage",
    `go--Linux-go1.27.4-${SUM_A}-${sha("1")}`,
    `go-test-Linux-go1.27.4-${SUM_A}-${sha("1")}-extra`,
  ].map((key, i) => ({ id: 100 + i, key, ref: "refs/heads/main", createdAt: `2026-11-0${i + 1}T10:00:00Z`, sizeInBytes: 1 }));
  // A real entry newer than all of them, in the series two of the look-alikes pretend to belong to: a
  // reader that is too loose would call them its older generations and delete them.
  const real = entry({ name: "test", at: "2026-11-09T10:00:00Z" });
  assert.deepEqual(selectSuperseded([...other, real]), []);
  assert.equal(parseKey("go-test-Linux-garbage"), null);
});

test("another operating system is another series", () => {
  const lin = entry({ name: "test", os: "Linux", at: "2026-11-01T10:00:00Z" });
  const mac = entry({ name: "test", os: "macOS", at: "2026-11-02T10:00:00Z" });
  assert.deepEqual(selectSuperseded([lin, mac]), []);
});

// Why: the prune must be repeatable and must never leave a name without an entry, also when two
// entries carry the same timestamp (one second resolution): exactly one of them stays, the same one
// on every run.
test("equal timestamps keep exactly one entry, the higher id", () => {
  const a = entry({ name: "e2e", commit: "1", at: "2026-11-12T03:21:00Z" });
  const b = entry({ name: "e2e", commit: "2", at: "2026-11-12T03:21:00Z" });
  assert.deepEqual(ids(selectSuperseded([a, b])), [a.id]);
  assert.deepEqual(ids(selectSuperseded([b, a])), [a.id]);
});

test("parseKey splits name, operating system and Go version from the version-free prefix", () => {
  assert.deepEqual(parseKey(`go-race-0-Linux-go1.27.3-${SUM_A}-${sha("f")}`), {
    series: "go-race-0-Linux",
    name: "race-0",
    os: "Linux",
    goVersion: "go1.27.3",
  });
});

test("nothing to do without caches, or with one per series", () => {
  assert.deepEqual(selectSuperseded([]), []);
  assert.deepEqual(selectSuperseded([entry({ name: "lint", at: "2026-11-01T10:00:00Z" })]), []);
});
