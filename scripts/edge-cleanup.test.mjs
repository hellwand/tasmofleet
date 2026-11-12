import { test } from "node:test";
import assert from "node:assert/strict";
import { selectEdgeDeletions } from "./edge-cleanup.mjs";

const d = (c) => `sha256:${c.repeat(64)}`;
const index = (tag, digest, ...kids) => ({ tags: [tag], digest, kids });

// Why (#728): a re-run of the same commit rebuilds a bit-identical arm64 image, so the new :edge
// index lists the same arm64 child as the index it replaced. Deleting "the previous edge and its
// children" then deleted a child of the live :edge; the nightly cleanup found the incomplete image
// ("partial") and removed :edge from ghcr. Only the digests the live tags do not use may go.
test("a child the new edge index still lists is kept (re-run of the same commit)", () => {
  const prevIndex = d("a"), prevAmd = d("b"), sharedArm = d("c");
  const newIndex = d("d"), newAmd = d("e");
  const got = selectEdgeDeletions({
    candidates: [prevIndex, prevAmd, sharedArm],
    tagged: [index("edge", newIndex, newAmd, sharedArm)],
  });
  assert.deepEqual(got, [prevIndex, prevAmd]);
});

// Why: the normal push. Nothing is shared, so the old edge is garbage in full; the step must still
// clean up (otherwise every push leaves three untagged versions behind).
test("a normal push deletes the previous index and all its children", () => {
  const got = selectEdgeDeletions({
    candidates: [d("a"), d("b"), d("c")],
    tagged: [index("edge", d("d"), d("e"), d("f"))],
  });
  assert.deepEqual(got, [d("a"), d("b"), d("c")]);
});

// Why: right after a release, :latest and :edge can point to the same image; a release must stay
// pullable, so neither its index nor its children may be deleted, whichever tag lists them.
test("the index and the children a release tag uses are kept", () => {
  const rel = d("1"), relAmd = d("2"), relArm = d("3");
  const got = selectEdgeDeletions({
    candidates: [rel, relAmd, relArm, d("a")],
    tagged: [
      { tags: ["0.34.1", "latest"], digest: rel, kids: [relAmd, relArm] },
      index("edge", d("d"), d("e"), relArm),
    ],
  });
  assert.deepEqual(got, [d("a")]);
});

// Why: any tagged index counts, not only edge and latest: an older release tag that shares a child
// with the previous edge must not lose it.
test("a child used by any other tagged index is kept", () => {
  const got = selectEdgeDeletions({
    candidates: [d("a"), d("b")],
    tagged: [index("edge", d("d"), d("e")), index("0.33.0", d("f"), d("b"))],
  });
  assert.deepEqual(got, [d("a")]);
});

// Why: edge did not move (a re-run that produced the very same index): the previous edge IS the
// live edge, and nothing may be deleted.
test("an unmoved edge deletes nothing", () => {
  const idx = d("a");
  const got = selectEdgeDeletions({
    candidates: [idx, d("b"), d("c")],
    tagged: [index("edge", idx, d("b"), d("c"))],
  });
  assert.deepEqual(got, []);
});

// Why: if the registry shows no :edge (the push failed, or the listing is incomplete), there is no
// proof of what the new edge uses, so the safe answer is to delete nothing.
test("without a tagged edge nothing is deleted", () => {
  const got = selectEdgeDeletions({
    candidates: [d("a"), d("b")],
    tagged: [{ tags: ["latest"], digest: d("1"), kids: [d("2")] }],
  });
  assert.deepEqual(got, []);
});

test("duplicate candidates are returned once", () => {
  const got = selectEdgeDeletions({ candidates: [d("a"), d("b"), d("a")], tagged: [index("edge", d("d"), d("e"))] });
  assert.deepEqual(got, [d("a"), d("b")]);
});

// Why: a single-platform image has no children; the list is legitimately empty.
test("an index without children is fine", () => {
  const got = selectEdgeDeletions({ candidates: [d("a")], tagged: [index("edge", d("d"))] });
  assert.deepEqual(got, [d("a")]);
});

// Why: malformed input (a failed lookup that produced an empty or garbled value) must stop the
// step instead of being read as "nothing is in use".
test("malformed input throws instead of guessing", () => {
  assert.throws(() => selectEdgeDeletions({ candidates: ["latest"], tagged: [] }), /not a sha256 digest/);
  assert.throws(() => selectEdgeDeletions({ candidates: [d("a")], tagged: [{ tags: ["edge"], digest: d("d") }] }), /kids must be an array/);
  assert.throws(() => selectEdgeDeletions({ candidates: [d("a")], tagged: null }), /tagged must be an array/);
  assert.throws(() => selectEdgeDeletions({ candidates: [d("a")], tagged: [{ tags: ["edge"], digest: "", kids: [] }] }), /not a sha256 digest/);
});
