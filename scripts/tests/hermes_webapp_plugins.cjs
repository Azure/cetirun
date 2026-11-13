// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const { stripTypeScriptTypes } = require("node:module");
const vm = require("node:vm");

async function load(source, bundled) {
  const effects = { evaluated: 0, registered: 0, bundled: 0, reads: 0, watches: 0, timers: 0 };
  const records = {};
  const atom = initial => ({ value: initial, get() { return this.value; }, set(value) { this.value = value; } });
  const root = "/synthetic/.hermes/desktop-plugins";
  const payload = "globalThis.effects.evaluated++; export default { id: 'disk', defaultEnabled: false, register() { globalThis.effects.registered++; } }";
  const desktop = {
    desktopPluginsRoot: async () => { effects.reads++; return root; },
    readDir: async path => ({ entries: path === root
      ? [{ isDirectory: true, name: "disk", path: root + "/disk" }]
      : [{ isDirectory: false, name: "plugin.js", path: root + "/disk/plugin.js" }] }),
    readPluginSource: async () => ({ text: payload }),
    watchPreviewFile: async () => { effects.watches++; return { id: "watch" }; },
    onPreviewFileChanged: () => { effects.watches++; },
  };
  const context = vm.createContext({
    effects, console: { info() {}, warn() {}, error() {} }, setTimeout, clearTimeout,
    Blob: class { constructor(parts) { this.parts = parts; } },
    URL: {
      createObjectURL: blob => "data:text/javascript;base64," + Buffer.from(blob.parts.join("")).toString("base64"),
      revokeObjectURL() {},
    },
    window: { hermesDesktop: desktop, setInterval() { effects.timers++; }, clearInterval() {} },
    document: { visibilityState: "visible" },
  });
  const exports = {
    atom, isReadFileErrorResult: result => !!result.error,
    installPluginSdk() {}, sdkImportMap: () => ({}), notifyError() {}, forgetPluginPreviewAccess() {},
    trackGatewayEventDisposers: (_track, action) => action(), createPluginContext: () => ({}),
    declaredCapabilities: plugin => plugin.capabilities ?? [],
    $pluginRecords: { get: () => records }, dropPlugin: id => { delete records[id]; },
    pluginActive: (_id, fallback) => fallback,
    publishPlugin: record => { records[record.id] = record; },
  };
  const dependencies = new vm.SyntheticModule(Object.keys(exports), function () {
    for (const [name, value] of Object.entries(exports)) this.setExport(name, value);
  }, { context });
  const loader = new vm.SourceTextModule(stripTypeScriptTypes(source), {
    context,
    importModuleDynamically: async url => {
      const code = Buffer.from(url.split(",")[1], "base64").toString();
      const plugin = new vm.SourceTextModule(code, { context });
      await plugin.link(() => { throw new Error("unexpected plugin import"); });
      await plugin.evaluate();
      return plugin;
    },
  });
  await loader.link(() => dependencies);
  await loader.evaluate();
  return {
    effects, records, payload, loader: loader.namespace,
    async discoverBundled() {
      const discovery = new vm.SourceTextModule(stripTypeScriptTypes(bundled), {
        context,
        initializeImportMeta: meta => {
          meta.glob = () => ({ "../plugins/core/plugin.ts": { default: {
            id: "core", register() { effects.bundled++; },
          } } });
        },
      });
      await discovery.link(name => name === "./runtime-loader" ? loader : dependencies);
      await discovery.evaluate();
      discovery.namespace.discoverBundledPlugins();
    },
  };
}

async function main() {
  const { original, patched, bundled } = JSON.parse(fs.readFileSync(0, "utf8"));
  const before = await load(original, bundled);
  await before.loader.discoverRuntimePlugins();
  assert.equal(before.effects.evaluated, 1, "unpatched disk source must reproduce top-level execution");
  assert.equal(before.effects.registered, 0, "disabled plugins still evaluate before their enable check");

  const after = await load(patched, bundled);
  after.loader.watchRuntimePlugins();
  await after.loader.discoverRuntimePlugins();
  await after.loader.discoverRuntimePlugins();
  for (const options of [{}, { defaultEnabled: false, packageName: "unified" }, { defaultEnabled: true }]) {
    assert.equal(await after.loader.loadRuntimePlugin(after.payload, "disk", options), null);
    assert.equal(after.records.disk.status, "error");
    assert.match(after.records.disk.error, /Runtime Desktop plugins are disabled/);
  }
  assert.deepEqual(after.effects, { evaluated: 0, registered: 0, bundled: 0, reads: 0, watches: 0, timers: 0 });
  assert.equal(after.loader.$diskPluginsScanPending.get(), false);
  await after.discoverBundled();
  assert.equal(after.effects.bundled, 1);
  assert.equal(after.records.core.status, "loaded");
  assert.equal(after.effects.reads, 0);
  assert.equal(after.effects.watches, 0);
  assert.equal(after.effects.timers, 0);
  console.log("Native loader: original trigger reproduced; managed execution blocked; bundled plugin retained.");
}

main().catch(error => { console.error(error); process.exitCode = 1; });
