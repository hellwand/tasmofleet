// Decides which manifests the "delete the previous edge" step of _image.yml may delete (#728).
//
// Every push to main moves :edge to a new multi-arch index. The index it replaced, and the
// per-platform manifests (children) that index listed, are then garbage. But a child is just a
// digest in the registry: when the new build produced an identical image for one platform (a re-run
// of the same commit gives the same VERSION build-arg and cache hits), the new index lists the very
// same child, and deleting it leaves the new :edge incomplete. The nightly cleanup then removes
// that "partial image", and :edge is gone.
//
// So the answer is derived from what the registry says now, not from the order of events:
// a candidate is deleted only if no tagged index, and no child of one, is that digest. The step
// lists every tagged index with its children (after the push, so :edge is the new one).
//
// Pure logic in selectEdgeDeletions(); the CLI reads {candidates, tagged} as JSON from stdin and
// prints the digests to delete as a JSON array. It touches no registry.
//   candidates: digests of the previous edge index and its children (what edge pointed to before)
//   tagged:     [{tags: string[], digest, kids: string[]}], one per index a tag points to now
import { fileURLToPath } from "node:url";

const DIGEST = /^sha256:[0-9a-f]{64}$/;

function digests(list, what) {
  if (!Array.isArray(list)) throw new Error(`${what} must be an array of digests`);
  for (const d of list) {
    if (typeof d !== "string" || !DIGEST.test(d)) throw new Error(`${what}: not a sha256 digest: ${JSON.stringify(d)}`);
  }
  return list;
}

// Returns the candidates that may be deleted, in the order given, without duplicates.
// Fails closed: anything unexpected throws (the step then deletes nothing), and without a tagged
// :edge there is no proof of what the new edge uses, so nothing is deleted either.
export function selectEdgeDeletions({ candidates, tagged }) {
  digests(candidates, "candidates");
  if (!Array.isArray(tagged)) throw new Error("tagged must be an array");
  const keep = new Set();
  let edge = false;
  for (const t of tagged) {
    if (!Array.isArray(t.tags)) throw new Error("tagged[].tags must be an array");
    keep.add(digests([t.digest], "tagged[].digest")[0]);
    for (const k of digests(t.kids, "tagged[].kids")) keep.add(k);
    if (t.tags.includes("edge")) edge = true;
  }
  if (!edge) return [];
  return [...new Set(candidates)].filter((d) => !keep.has(d));
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const chunks = [];
  for await (const c of process.stdin) chunks.push(c);
  console.log(JSON.stringify(selectEdgeDeletions(JSON.parse(Buffer.concat(chunks).toString()))));
}
