import { test, expect } from "../support/fixtures";
import {
  App,
  Devenv,
  freePort,
  startApp,
  startDevenv,
  writeTasmoAdminDir,
} from "../support/stack";

// #726: the Devices list got about 90 cell swaps a second with the 60-device fixture, and the page fell
// behind its server by 10 s and more. Every health event of a device sends six fragments (the phone
// cards repeat four of them), changed or not, and each swap runs the page's swap scripts. These guards
// count what the page does with the stream, not how long it takes, so they do not depend on the speed
// of the machine.
const adam = { "X-authentik-username": "adam", "X-authentik-groups": "adm" };
let app: App;
let env: Devenv;

test.beforeAll(async () => {
  test.setTimeout(120_000);
  env = await startDevenv();
  const fwPort = await freePort();
  app = await startApp({
    env: {
      TASMOFLEET_AUTH_MODE: "header",
      TASMOFLEET_TRUSTED_PROXIES: "127.0.0.1/32",
      TASMOFLEET_ADMIN_GROUPS: "adm",
      TASMOFLEET_MQTT_URL: env.vars.TASMOFLEET_MQTT_URL,
      TASMOFLEET_POLL_INTERVAL: "1s", // a health event per device and second: repeats come quickly
      TASMOFLEET_FIRMWARE_UPSTREAM_OVERRIDE:
        env.vars.DEVENV_UPSTREAM_GITHUB_URL,
      TASMOFLEET_FIRMWARE_LISTEN: `127.0.0.1:${fwPort}`,
      TASMOFLEET_FIRMWARE_BASE_URL: `http://127.0.0.1:${fwPort}`,
    },
  });
  app.cli(["import", "tasmoadmin", writeTasmoAdminDir(env.devices)]);
});
test.afterAll(async () => {
  await app?.stop();
  await env?.stop();
});

type Seen = { messages: number; changes: number; swaps: number };

test("a live cell is swapped only when its fragment differs from the last one it received", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.setExtraHTTPHeaders(adam);
  // The counters sit on window in the capture phase, so they see every message and swap whatever the
  // page's own listeners do with it. A "change" is a message whose fragment differs from the previous
  // one of the same element (the first one of an element counts: it has nothing to compare with).
  await page.addInitScript(() => {
    const seen: Seen = { messages: 0, changes: 0, swaps: 0 };
    const last = new WeakMap<Element, string>();
    (window as unknown as { __seen: Seen }).__seen = seen;
    window.addEventListener(
      "htmx:sseBeforeMessage",
      (e) => {
        const t = e.target as Element;
        if (!t.matches?.("table [sse-swap], li.dcard [sse-swap]")) return;
        const data = (e as CustomEvent).detail.data as string;
        seen.messages++;
        if (last.get(t) !== data) seen.changes++;
        last.set(t, data);
      },
      true,
    );
    window.addEventListener(
      "htmx:afterSwap",
      (e) => {
        const t = e.target as Element;
        if (t.matches?.("table [sse-swap], li.dcard [sse-swap]")) seen.swaps++;
      },
      true,
    );
  });
  await page.goto(app.url + "/devices");
  await expect(page.locator("#devtable tbody tr").first()).toBeAttached({
    timeout: 40_000,
  });
  // enough repeated fragments to tell "skipped" from "not seen yet"
  const read = () =>
    page.evaluate(() => (window as unknown as { __seen: Seen }).__seen);
  await expect
    .poll(
      async () => {
        const s = await read();
        return s.messages - s.changes;
      },
      { timeout: 60_000, intervals: [500] },
    )
    .toBeGreaterThanOrEqual(40);
  const s = await read();
  expect(
    s.swaps,
    `${s.messages} messages, ${s.changes} of them differ from the previous one of their cell`,
  ).toBeLessThanOrEqual(s.changes);

  // The memo must not hide a cell that was changed by something else: a swap of the element (a power
  // toggle's response lands in the same cell) drops it, and the next fragment of the stream puts the
  // real content back.
  const cell = page.locator("#devtable tbody tr td.pcell").first();
  // read in the same task as the swap: with a health event a second the stream may put the content back at once
  const swapped = await cell.evaluate((el) => {
    // @ts-expect-error htmx is a page global
    window.htmx.swap(el, "<span>stale</span>", { swapStyle: "innerHTML" });
    return el.textContent;
  });
  expect(swapped).toContain("stale");
  await expect(cell).not.toContainText("stale", { timeout: 15_000 });
});

// syncAll (select.js) walks the whole document: it shows the select-all controls and fills the bulk bar
// (#594). It runs when a swap brings those controls in (the list, the scan results), not for the single
// cells of the live stream, which settle one by one (htmx:afterSettle still runs it for them).
test("a live cell swap does not run the document-wide select sync, a list reload does", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.setExtraHTTPHeaders(adam);
  type Sync = { runs: number; cell: number[]; list: number[] };
  await page.addInitScript(() => {
    // syncAll asks the document for the select-all controls once per run; nothing else does.
    const w = window as unknown as { __sync: Sync };
    w.__sync = { runs: 0, cell: [], list: [] };
    const qsa = document.querySelectorAll.bind(document);
    document.querySelectorAll = ((sel: string) => {
      if (sel === "[data-select-all]") w.__sync.runs++;
      return qsa(sel);
    }) as typeof document.querySelectorAll;
  });
  await page.goto(app.url + "/devices");
  await expect(
    page.locator("#devtable [data-select-all]").first(),
  ).toBeAttached({ timeout: 40_000 });
  // Registered now, so it runs after the page's own document listeners (like swap-no-flash.spec.ts):
  // the capture listener on window notes the run count before any of them, this one after all of them.
  await page.evaluate(() => {
    const w = window as unknown as { __sync: Sync };
    let before = 0;
    window.addEventListener(
      "htmx:afterSwap",
      () => (before = w.__sync.runs),
      true,
    );
    document.addEventListener("htmx:afterSwap", (e) => {
      const t = e.target as Element;
      const ran = w.__sync.runs - before;
      if (t.matches?.("table [sse-swap], li.dcard [sse-swap]"))
        w.__sync.cell.push(ran);
      else if (t.id === "devtable") w.__sync.list.push(ran);
    });
    // The reload makes new cells, and the first fragment of a new cell is always swapped.
    // @ts-expect-error htmx is a page global
    window.htmx.trigger("#devtable", "sse:resync");
  });
  const read = () =>
    page.evaluate(() => (window as unknown as { __sync: Sync }).__sync);
  await expect
    .poll(async () => (await read()).list.length, { timeout: 15_000 })
    .toBeGreaterThan(0);
  await expect
    .poll(async () => (await read()).cell.length, {
      timeout: 60_000,
      intervals: [500],
    })
    .toBeGreaterThanOrEqual(5);
  const got = await read();
  expect(
    got.cell.filter((n) => n > 0),
    "syncAll runs during the afterSwap of live cells",
  ).toEqual([]);
  expect(
    got.list.every((n) => n > 0),
    "the list reload still syncs at afterSwap (#594)",
  ).toBe(true);
});
