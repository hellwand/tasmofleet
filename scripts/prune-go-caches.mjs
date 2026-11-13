// Decides which Go caches of the repository the cache-prune workflow may delete (#692).
//
// .github/actions/go saves one cache per job kind and run on main under the key
//   go-<name>-<os>-<GOVERSION>-<hash of go.sum>-<commit sha>
// The commit makes every key new, the Go version and the go.sum hash start a series that the
// restore-keys of the next run can no longer reach. Nothing removes the entries behind, and a
// floating `go 1.27` that moved from 1.27.3 to 1.27.4 orphaned a whole series next to the live one.
//
// A series is `go-<name>-<os>-` WITHOUT the Go version and the go.sum hash (go-race-0 and go-race-1
// are two series). Per series only the newest entry of refs/heads/main stays; every older one is
// selected. Entries of other refs and keys that are not of this shape (buildkit, CodeQL, npm,
// Tasmota sources) are never selected.
//
// Pure logic in selectSuperseded(); the CLI reads the JSON of
//   gh cache list --json id,key,ref,createdAt,sizeInBytes
// from stdin and prints the entries to delete as JSON. It touches no cache.
import { fileURLToPath } from "node:url";

const MAIN = "refs/heads/main";
const KEY = /^go-(.+?)-(Linux|macOS|Windows)-(go[^-]+)-[0-9a-f]{64}-[0-9a-f]{40}$/;

// The series a key belongs to, or null when the key is not one of .github/actions/go.
export function parseKey(key) {
  const m = KEY.exec(key);
  if (!m) return null;
  const [, name, os, goVersion] = m;
  return { series: `go-${name}-${os}`, name, os, goVersion };
}

// entries: [{id, key, ref, createdAt}]. Returns the entries to delete.
export function selectSuperseded(entries) {
  const bySeries = new Map();
  for (const e of entries) {
    if (e.ref !== MAIN) continue;
    const k = parseKey(e.key);
    if (!k) continue;
    if (!bySeries.has(k.series)) bySeries.set(k.series, []);
    bySeries.get(k.series).push(e);
  }
  // Newest first; at equal times the higher id, so the same entry stays on every run.
  const newestFirst = (a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt) || b.id - a.id;
  const doomed = [];
  for (const list of bySeries.values()) doomed.push(...list.sort(newestFirst).slice(1));
  return doomed;
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const chunks = [];
  for await (const c of process.stdin) chunks.push(c);
  console.log(JSON.stringify(selectSuperseded(JSON.parse(Buffer.concat(chunks).toString()))));
}
