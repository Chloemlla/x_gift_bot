import { createHash } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { gzipSync } from "node:zlib";
import { build } from "esbuild";

await build({
  entryPoints: {
    appearance: "frontend/src/appearance.ts",
    app: "frontend/src/app.tsx",
    admin: "frontend/src/admin.tsx",
  },
  outdir: "internal/site/assets",
  bundle: true,
  minify: true,
  format: "esm",
  target: ["es2022"],
  legalComments: "eof",
  define: { "process.env.NODE_ENV": '"production"' },
  logLevel: "info",
});

// Embed compressed assets as well, so direct Go and preview serving match Caddy.
for (const name of ["app", "admin", "appearance"]) {
  const path = `internal/site/assets/${name}.js`;
  await writeFile(`${path}.gz`, gzipSync(await readFile(path), { level: 9 }));
}

const colors = JSON.parse(await readFile("frontend/src/colors.json", "utf8"));
const replacements = {
  __LIGHT_BACKGROUND__: colors.light.background.default,
  __DARK_BACKGROUND__: colors.dark.background.default,
};
for (const name of ["app", "admin", "appearance"]) {
  replacements[`__${name.toUpperCase()}_VERSION__`] = createHash("sha256")
    .update(await readFile(`internal/site/assets/${name}.js`))
    .digest("hex")
    .slice(0, 12);
}
for (const name of ["index", "admin"]) {
  let html = await readFile(`frontend/pages/${name}.html`, "utf8");
  for (const [placeholder, value] of Object.entries(replacements))
    html = html.replaceAll(placeholder, value);
  await writeFile(`internal/site/assets/${name}.html`, html);
}
