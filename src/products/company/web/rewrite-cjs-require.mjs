import { createRequire as __nodeCreateRequire } from "node:module";
__nodeCreateRequire(import.meta.url);
import { isBuiltin } from "node:module";
import { promises } from "node:fs";
import path from "node:path";
//#region rewrite-cjs-require
const SCAN_EXTS = [".mjs", ".js"];
const REQUIRE_IMPORT_RE = /import\s*\{([^}]+)\}\s*from\s*["'][^"']+["']/g;
const ALIAS_RE = /(?:[\w$]+\s+as\s+)?(require_[A-Za-z0-9_$]+)/g;
const REQUIRE_CALL_RE = /\b__require\s*\(\s*["']([^"']+)["']\s*\)/g;
async function rewriteCjsRequireOnCompiled(nitro) {
	const serverDir = nitro.options.output.serverDir;
	const files = [];
	await walk(serverDir, files);
	let replaced = 0;
	let touchedFiles = 0;
	const missed = /* @__PURE__ */ new Map();
	for (const file of files) {
		const original = await promises.readFile(file, "utf8");
		if (!original.includes("__require(")) continue;
		const result = rewriteChunk(original);
		if (result.replacements > 0) {
			await promises.writeFile(file, result.source);
			replaced += result.replacements;
			touchedFiles += 1;
		}
		for (const spec of result.missed) missed.set(spec, (missed.get(spec) ?? 0) + 1);
	}
	if (missed.size > 0) {
		const summary = [...missed.entries()].map(([spec, n]) => `${spec}×${n}`).join(", ");
		console.warn(`[guardian:rewrite-cjs-require] ${missed.size} external __require call(s) unrewritten: ${summary}`);
	}
	if (replaced > 0) console.log(`[guardian:rewrite-cjs-require] rewrote ${replaced} __require() call(s) across ${touchedFiles} chunk(s) under ${path.relative(nitro.options.rootDir, serverDir) || serverDir}`);
}
function rewriteChunk(source) {
	const aliases = collectAliases(source);
	const missed = /* @__PURE__ */ new Set();
	let replacements = 0;
	return {
		source: source.replace(REQUIRE_CALL_RE, (full, spec) => {
			if (isBuiltin(spec)) return full;
			const symbol = bundledSymbolFor(spec);
			if (aliases.has(symbol)) {
				replacements += 1;
				return `${symbol}()`;
			}
			missed.add(spec);
			return full;
		}),
		replacements,
		missed
	};
}
function collectAliases(source) {
	const aliases = /* @__PURE__ */ new Set();
	for (const stmt of source.matchAll(REQUIRE_IMPORT_RE)) {
		const inside = stmt[1];
		if (inside === void 0) continue;
		for (const alias of inside.matchAll(ALIAS_RE)) {
			const symbol = alias[1];
			if (symbol !== void 0) aliases.add(symbol);
		}
	}
	return aliases;
}
function bundledSymbolFor(spec) {
	return `require_${(spec.split("/").pop() ?? spec).replace(/[^A-Za-z0-9_]/g, "_")}`;
}
async function walk(dir, into) {
	let entries;
	try {
		entries = await promises.readdir(dir, { withFileTypes: true });
	} catch (error) {
		if (error.code === "ENOENT") return;
		throw error;
	}
	for (const entry of entries) {
		const full = path.join(dir, entry.name);
		if (entry.isDirectory()) await walk(full, into);
		else if (SCAN_EXTS.some((ext) => entry.name.endsWith(ext))) into.push(full);
	}
}
//#endregion
export { rewriteCjsRequireOnCompiled };
