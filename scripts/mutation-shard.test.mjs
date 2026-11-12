// tools/mutation-shard.sh deals the files of the MUTATION packages out to the CI mutation shards by
// estimated cost: runnable mutants (tools/mutation-weights.txt) x seconds per mutant. A file without a
// weights entry used to count as ONE mutant. A file added after the last refresh of the weights is
// exactly that, so a 355-line file (about 85 mutants, minutes of work) was dealt as if it were free: on
// 2026-11-09 shard 4/4 took over the 30 minute job limit because the new files ota/templatejob.go and
// policy/template.go (about 200 mutants together) all landed on the shard that already held the slowest
// package, and the weekly run was cancelled (#729). The estimate for an unweighted file now follows its
// size, so the shards stay balanced between two refreshes of the weights.
import { test } from "node:test";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");

// A plan for internal/cmdpolicy (cmdpolicy.go is its big file, scrub.go and policyrule.go are small) with
// the given weights file content, in two shards. Returns the file names of each shard.
function plan(weights) {
  const dir = mkdtempSync(join(tmpdir(), "mutation-shard-"));
  try {
    const file = join(dir, "weights.txt");
    writeFileSync(file, weights);
    const r = spawnSync("tools/mutation-shard.sh", ["plan", "2"], {
      cwd: root,
      encoding: "utf8",
      env: {
        ...process.env,
        MUTATION_PKGS: "./internal/cmdpolicy",
        MUTATION_WEIGHTS: file,
      },
    });
    assert.equal(r.status, 0, r.stderr);
    return r.stdout
      .trim()
      .split("\n")
      .map((l) => l.split("(estimate):")[1].trim().split(/\s+/));
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

test("a file without a weights entry is dealt by its size, not as one mutant", () => {
  // cmdpolicy.go has no entry, as a file added after the last refresh. If it counted as one mutant it
  // would share a shard with the small files; it is the biggest file of the package, so it must stand
  // alone on the lighter side of the deal.
  const shards = plan(
    [
      "cost internal/cmdpolicy 27",
      "internal/cmdpolicy scrub.go 24",
      "internal/cmdpolicy policyrule.go 5",
      "internal/cmdpolicy doc.go 0",
      "",
    ].join("\n"),
  );
  const big = shards.find((s) => s.includes("internal/cmdpolicy/cmdpolicy.go"));
  assert.ok(big, "cmdpolicy.go is in a shard");
  assert.deepEqual(big, ["internal/cmdpolicy/cmdpolicy.go"]);
});

test("a file with an entry keeps its measured weight, even when it is small", () => {
  // The estimate by size is only for files nobody measured: an entry of 0 mutants (a file of constants
  // or types) must stay 0, or every doc.go would weigh as much as its lines.
  const shards = plan(
    [
      "cost internal/cmdpolicy 27",
      "internal/cmdpolicy cmdpolicy.go 127",
      "internal/cmdpolicy scrub.go 24",
      "internal/cmdpolicy policyrule.go 5",
      "internal/cmdpolicy doc.go 0",
      "",
    ].join("\n"),
  );
  assert.deepEqual(shards[0], ["internal/cmdpolicy/cmdpolicy.go"]);
  assert.deepEqual(shards[1].sort(), [
    "internal/cmdpolicy/doc.go",
    "internal/cmdpolicy/policyrule.go",
    "internal/cmdpolicy/scrub.go",
  ]);
});
