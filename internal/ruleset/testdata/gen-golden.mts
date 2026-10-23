// Writes the parity goldens: ct-cli's own normalizeRuleset over every raw
// `*.ruleset.json` in IN_DIR, serialised canonically (keys sorted recursively),
// to OUT_DIR/<name>.golden.json. The Go normalizer must reproduce them byte for byte.
//
//   CT_CLI=/path/to/ct-cli npx --prefix "$CT_CLI" tsx gen-golden.mts IN_DIR OUT_DIR
//
// Read-only on ct-cli; it only imports src/engine/dynamic.ts.
import { readdirSync, readFileSync, writeFileSync, mkdirSync } from "node:fs";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

const [inDir, outDir] = process.argv.slice(2);
if (!inDir || !outDir || !process.env.CT_CLI) throw new Error("usage: CT_CLI=... gen-golden.mts IN_DIR OUT_DIR");
const { normalizeRuleset } = await import(pathToFileURL(join(process.env.CT_CLI, "src/engine/dynamic.ts")).href);

function canon(v: unknown): unknown {
  if (Array.isArray(v)) return v.map(canon);
  if (v && typeof v === "object") {
    const o = v as Record<string, unknown>;
    return Object.fromEntries(Object.keys(o).sort().map((k) => [k, canon(o[k])]));
  }
  return v;
}

mkdirSync(outDir, { recursive: true });
let n = 0;
for (const f of readdirSync(inDir).filter((f) => f.endsWith(".ruleset.json")).sort()) {
  const raw = JSON.parse(readFileSync(join(inDir, f), "utf8"));
  const out = JSON.stringify(canon(normalizeRuleset(raw)));
  writeFileSync(join(outDir, f.replace(/\.ruleset\.json$/, ".golden.json")), out);
  n++;
}
console.log(`${n} goldens written to ${outDir}`);
