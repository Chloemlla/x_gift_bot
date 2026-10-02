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
