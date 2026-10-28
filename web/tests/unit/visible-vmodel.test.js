// Regression test for the FolderPicker "dead dialog" bug: every wrapper
// dialog/panel under src/components declares a `visible` prop and emits
// `update:visible`. Binding one with a bare `v-model` instead of
// `v-model:visible` compiles to an undeclared `modelValue` attr that silently
// falls through to the inner el-dialog — the dialog still opens (so it looks
// half-working), but the wrapper's own `visible` watcher never fires (the
// folder picker loaded nothing on open) and its `update:visible` emits are
// unheard (confirm/cancel buttons went dead, the confirm button stuck on its
// loading state). This test pins the binding convention across ALL views.

import { describe, it, expect } from "vitest";
import { parse } from "@vue/compiler-sfc";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const webRoot = join(dirname(fileURLToPath(import.meta.url)), "../..");
const SRC = join(webRoot, "src");

function walkVueFiles(dir) {
  const out = [];
  for (const entry of readdirSync(dir)) {
    const p = join(dir, entry);
    if (statSync(p).isDirectory()) out.push(...walkVueFiles(p));
    else if (entry.endsWith(".vue")) out.push(p);
  }
  return out;
}

const pascalToKebab = (s) => s.replace(/([a-z0-9])([A-Z])/g, "$1-$2").toLowerCase();

// Components whose public visibility contract is the `visible` prop.
const visibleComponents = new Set(
  walkVueFiles(join(SRC, "components"))
    .concat(walkVueFiles(join(SRC, "layout")))
    .filter((f) => readFileSync(f, "utf8").includes("visible: { type: Boolean"))
    .map((f) => pascalToKebab(f.replace(/\.vue$/, "").split(/[\\/]/).pop()))
);

function templateOf(file) {
  const { descriptor, errors } = parse(readFileSync(file, "utf8"), { filename: file });
  if (errors.length) throw new Error(`parse error in ${file}: ${errors[0].message}`);
  return descriptor.template?.ast || null;
}

// Walk the template AST collecting every v-model directive prop with its tag.
function collectModels(node, tag, out) {
  if (!node) return out;
  if (node.tag) tag = node.tag;
  for (const prop of node.props || []) {
    if (prop.type === 7 && prop.name === "model") out.push({ tag, arg: prop.arg?.content ?? "" });
  }
  for (const child of node.children || []) collectModels(child, tag, out);
  return out;
}

describe("wrapper components with a visible prop are bound with v-model:visible", () => {
  it("every usage across views/layout binds model arg 'visible', never a bare v-model", () => {
    const offenders = [];
    for (const file of walkVueFiles(SRC)) {
      const ast = templateOf(file);
      if (!ast) continue;
      for (const { tag, arg } of collectModels(ast, "", [])) {
        if (visibleComponents.has(tag) && arg !== "visible") {
          offenders.push(`${file}: <${tag} v-model${arg ? ":" + arg : ""} ...>`);
        }
      }
    }
    expect(offenders, offenders.join("\n")).toEqual([]);
  });

  it("actually guards the dialogs that regressed (self-check the component set)", () => {
    for (const t of ["folder-picker", "share-dialog", "viewer-dialog", "files-dialog", "action-sheet"]) {
      expect(visibleComponents.has(t), `missing from guard set: ${t}`).toBe(true);
    }
  });
});
