// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { createHash } from "node:crypto";
import { copyFileSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

export const SOURCE_FILE = "dist/builtin-openclaw-q1hiFm14.mjs";
export const SOURCE_SHA256 = "e4504516aef56cd293e11369e420148c5ad60c80e60128ed64bfb57fb7239e25";
const PREFIX = 'import { activateSelectedSkills } from "./cetirun-skill-activation.mjs";\n'
  + 'import { p as runWithToolExecutionValidation } from "./agent-tools.before-tool-call-D6M7yTR3.mjs";\n'
  + 'import { t as canonicalizePath } from "./paths-CIwQeLl6.mjs";\n';
const RESOURCES_BEFORE = "\t\tconst skillReadResources = params.sandbox?.enabled ? void 0 : resolveSkillResourceCandidates(skillsSnapshot);\n";
const RESOURCES_AFTER = "\t\tconst skillReadResources = params.sandbox?.enabled ? void 0 : resolveSkillResourceCandidates(skillsSnapshot)\n"
  + "\t\t\t?? (params.attempt.explicitSkillSelections?.length ? candidates.filter((candidate) => codeModeSkills.some((skill) => skill.source.filePath === candidate.filePath)) : void 0);\n";
const CATALOG_BEFORE = "\t\tcodeModeControlsEnabledForRun,\n\t\tcodeModeSkills,\n\t\tcomputerContextEpoch,\n";
const CATALOG_AFTER = "\t\tcodeModeControlsEnabledForRun,\n\t\tcodeModeSkills,\n\t\tselectedSkillCatalog: attempt.explicitSkillSelections?.length ? params.codeModeSkills.map((skill) => ({ ...skill, canonicalPath: canonicalizePath(skill.source.filePath) })) : [],\n\t\tcomputerContextEpoch,\n";
const LOAD_BEFORE = "\t\tconst promptContext = await prepareEmbeddedAttemptPromptContext({\n\t\t\tsessionVersion: sessionManager.getHeader()?.version,\n\t\t\tattempt,\n";
const LOAD_AFTER = `\t\tconst recordSkillActivation = async (receipts) => {
\t\t\tawait withOwnedTranscriptWrite(() => withSessionManagerWrite(sessionManager, () => {
\t\t\t\trunAbortController.signal.throwIfAborted();
\t\t\t\tfor (const receipt of receipts) sessionManager.appendCustomEntry("cetirun.skill-activation", receipt);
\t\t\t}));
\t\t};
\t\tlet selectedSkillActivation;
\t\tconst prepareSkillPromptContext = (activation) => prepareEmbeddedAttemptPromptContext({
\t\t\tsessionVersion: sessionManager.getHeader()?.version,
\t\t\tattempt: activation ? {
\t\t\t\t...attempt,
\t\t\t\truntimeContextFragments: [...attempt.runtimeContextFragments ?? [], ...activation.fragments]
\t\t\t} : attempt,
`;
const CONTEXT_BEFORE = "\t\t\ttoolResultPromptProjectionState\n\t\t});\n\t\tif (runAbortController.signal.aborted) throw createAbortableError(runAbortController.signal);\n";
const CONTEXT_AFTER = "\t\t\ttoolResultPromptProjectionState\n\t\t});\n\t\tlet promptContext = await prepareSkillPromptContext();\n\t\tif (runAbortController.signal.aborted) throw createAbortableError(runAbortController.signal);\n";
const ADMITTED_BEFORE = "\t\tif (!skipPromptSubmission) {\n\t\t\tconst { resolvedApiKey } = attempt;\n";
const ADMITTED_AFTER = `\t\tif (!skipPromptSubmission) {
\t\t\tselectedSkillActivation = await activateSelectedSkills({
\t\t\t\tselections: attempt.explicitSkillSelections,
\t\t\t\trunId: attempt.runId,
\t\t\t\tgetState: () => ({
\t\t\t\t\ttools: promptToolPolicy.current.effectiveTools,
\t\t\t\t\tcallableToolNames: promptToolPolicy.current.callableToolNames,
\t\t\t\t\tcatalog: prepared.toolBase.selectedSkillCatalog,
\t\t\t\t\tcache: prepared.toolBase.skillInstructionDeliveryCache,
\t\t\t\t\tsignal: prepared.toolBase.toolAbortSignal
\t\t\t\t}),
\t\t\t\twithValidation: runWithToolExecutionValidation,
\t\t\t\tescapeText: escapeInternalRuntimeContextDelimiters,
\t\t\t\tprojectFragments: (fragments) => usesEscapedRuntimeContext(sessionManager.getHeader()?.version)
\t\t\t\t\t? projectRuntimeContextFragments(fragments) : fragments.map((fragment) => fragment.text).join("\\n\\n")
\t\t\t});
\t\t\tif (selectedSkillActivation) {
\t\t\t\tawait recordSkillActivation(selectedSkillActivation.receipts);
\t\t\t\tpromptContext = await prepareSkillPromptContext(selectedSkillActivation);
\t\t\t}
\t\t\tconst { resolvedApiKey } = attempt;
`;
const SUBMIT_BEFORE = "\t\tif (!state.skipPromptSubmission) await submitEmbeddedAttemptPrompt({\n";
const SUBMIT_AFTER = SUBMIT_BEFORE + `\t\t\tverifySelectedSkillContext: selectedSkillActivation ? async (context) => {
\t\t\t\tawait recordSkillActivation(selectedSkillActivation.verifyContext(context));
\t\t\t} : void 0,
\t\t\tcheckSelectedSkillAuthority: selectedSkillActivation?.checkAuthority,
`;
const VERIFY_BEFORE = "\t\t\tassertSteeringCurrent();\n\t\t\tconst stream = await baseStreamFn(model, context, options);\n";
const VERIFY_AFTER = "\t\t\tassertSteeringCurrent();\n"
  + "\t\t\tif (!activeSession.isCompacting) await input.verifySelectedSkillContext?.(context);\n"
  + "\t\t\tif (!activeSession.isCompacting) input.checkSelectedSkillAuthority?.();\n"
  + "\t\t\tconst stream = await baseStreamFn(model, context, options);\n";
// The isolated finalizer has neither the selected catalog nor an authorized
// reader. Let its existing no-model fallback preserve completed tool work.
const FINALIZE_BEFORE = "\t\tfinalizeSettledTurn: async ({ attempt }) => {\n\t\t\tconst result = await runEmbeddedAttempt(buildRestrictedFinalizationAttempt(attempt));\n";
const FINALIZE_AFTER = "\t\tfinalizeSettledTurn: async ({ attempt }) => {\n"
  + '\t\t\tif (attempt.explicitSkillSelections?.length) throw new Error("Selected skill activation cannot be verified in tool-free finalization");\n'
  + "\t\t\tconst result = await runEmbeddedAttempt(buildRestrictedFinalizationAttempt(attempt));\n";
export const EDITS = [[RESOURCES_BEFORE, RESOURCES_AFTER], [CATALOG_BEFORE, CATALOG_AFTER], [LOAD_BEFORE, LOAD_AFTER], [CONTEXT_BEFORE, CONTEXT_AFTER], [ADMITTED_BEFORE, ADMITTED_AFTER], [SUBMIT_BEFORE, SUBMIT_AFTER], [VERIFY_BEFORE, VERIFY_AFTER], [FINALIZE_BEFORE, FINALIZE_AFTER]];
const count = (text, part) => text.split(part).length - 1;

export function original(source) {
  if (!source.startsWith(PREFIX)) return source;
  return EDITS.reduce((text, [before, after]) => text.replace(after, before), source.slice(PREFIX.length));
}

export function patch(source) {
  for (const [before, after] of EDITS) {
    if (count(source, before) !== 1 || count(source, after) !== 0) throw new Error("OpenClaw skill activation integration point changed");
    source = source.replace(before, after);
  }
  return PREFIX + source;
}

export const READ_FILE = "dist/agent-tools.read-g6MfsPCZ.mjs";
const READ_SHA256 = "0f9d846f36cf48967238a8ab9d46ad3ed5767aa4955afacaa5d20f55d495a825";
const READ_PREFIX = 'import { recordCompleteSkillRead } from "./cetirun-skill-activation.mjs";\n';
const READ_BEFORE = "\t\t\t\tsettleDelivery(true);\n\t\t\t\treturn result;\n";
const READ_AFTER = "\t\t\t\trecordCompleteSkillRead(delivery, result, toolCallId, instructionPath);\n" + READ_BEFORE;

export function apply(root, { check = false } = {}) {
  const file = join(root, SOURCE_FILE);
  const source = readFileSync(file, "utf8");
  const published = original(source);
  if (createHash("sha256").update(published).digest("hex") !== SOURCE_SHA256) throw new Error("Unsupported OpenClaw skill activation source; review the pinned integration");
  const changed = patch(published);
  const readerFile = join(root, READ_FILE);
  const reader = readFileSync(readerFile, "utf8");
  const originalReader = reader.startsWith(READ_PREFIX) ? reader.slice(READ_PREFIX.length).replace(READ_AFTER, READ_BEFORE) : reader;
  if (createHash("sha256").update(originalReader).digest("hex") !== READ_SHA256 || count(originalReader, READ_BEFORE) !== 1) throw new Error("Unsupported OpenClaw complete skill reader; review the pinned integration");
  const changedReader = READ_PREFIX + originalReader.replace(READ_BEFORE, READ_AFTER);
  const helperSource = join(dirname(fileURLToPath(import.meta.url)), "skill-activation.mjs");
  const helperTarget = join(root, "dist/cetirun-skill-activation.mjs");
  if (check && source !== published && !readFileSync(helperTarget).equals(readFileSync(helperSource))) throw new Error("OpenClaw skill activation helper does not match the reviewed source");
  if (!check) {
    if (changedReader !== reader) writeFileSync(readerFile, changedReader);
    copyFileSync(helperSource, helperTarget);
    if (changed !== source) writeFileSync(file, changed);
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  if (!process.argv[2]) throw new Error("Provide the pinned OpenClaw package root");
  apply(process.argv[2], { check: process.argv.includes("--check") });
}
