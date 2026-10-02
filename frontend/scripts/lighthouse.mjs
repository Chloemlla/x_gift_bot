import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { parseArgs } from "node:util";
import lighthouse from "lighthouse";
import { launch } from "chrome-launcher";

const { values } = parseArgs({
  options: {
    url: { type: "string", default: "http://127.0.0.1:4173/" },
    runs: { type: "string", default: "3" },
    dark: { type: "boolean", default: false },
  },
});
const url = new URL(values.url);
const runs = Number(values.runs);
if (!Number.isInteger(runs) || runs < 1 || runs > 10)
  throw new Error("--runs must be 1–10");
const directory = ".artifacts/lighthouse";
await mkdir(directory, { recursive: true });
const name = `${url.pathname === "/admin" ? "admin" : "redeem"}-${values.dark ? "dark" : "light"}`;
const results = [];
for (let index = 1; index <= runs; index++) {
  // New empty profile, no extensions, no warm cache or saved theme on every run.
  const profile = await mkdtemp(join(tmpdir(), "xgift-lighthouse-"));
  let chrome;
  try {
    chrome = await launch({
      userDataDir: profile,
      chromeFlags: [
        "--headless",
        "--disable-extensions",
        ...(values.dark ? ["--force-dark-mode"] : []),
      ],
    });
    const result = await lighthouse(url.href, {
      port: chrome.port,
      logLevel: "error",
      output: ["html", "json"],
      onlyCategories: ["performance", "accessibility", "best-practices", "seo"],
      // Keep Lighthouse's standard mobile device, network and CPU throttling.
    });
    if (!result || result.lhr.runtimeError)
      throw new Error(result?.lhr.runtimeError?.message || "No audit result");
    const prefix = `${directory}/${name}-${index}`;
    await writeFile(`${prefix}.html`, result.report[0]);
    await writeFile(`${prefix}.json`, result.report[1]);
    const scores = Object.fromEntries(
      Object.entries(result.lhr.categories).map(([key, category]) => [
        key,
        Math.round(category.score * 100),
      ]),
    );
    const entry = {
      run: index,
      scores,
      metrics: Object.fromEntries(
        [
          "first-contentful-paint",
          "largest-contentful-paint",
          "total-blocking-time",
          "cumulative-layout-shift",
        ].map((key) => [key, result.lhr.audits[key].numericValue]),
      ),
      report: `${prefix}.html`,
      warnings: result.lhr.runWarnings,
    };
    results.push(entry);
    console.log(JSON.stringify(entry));
    if (Object.values(scores).some((score) => score < 97)) process.exitCode = 1;
  } finally {
    await chrome?.kill();
    await rm(profile, { recursive: true, force: true });
  }
}
await writeFile(
  `${directory}/${name}-summary.json`,
  JSON.stringify(
    {
      url: url.href,
      theme: values.dark ? "system dark" : "system default",
      profile: "new empty temporary profile for each run; extensions disabled",
      preset: "Lighthouse default mobile simulated throttling",
      target: 97,
      results,
    },
    null,
    2,
  ),
);
if (process.exitCode)
  console.error(
    "At least one category scored below 97. See .artifacts/lighthouse reports.",
  );
