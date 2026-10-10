import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

const scriptPath = fileURLToPath(import.meta.url);

// Reject encoded Markdown separators, not legitimate code examples.
export function pullRequestBodyFormatViolations(body) {
  if (typeof body !== "string" || !body.trim()) {
    return ["Pull request description must contain substantive Markdown text."];
  }
  const prose = body
    .replace(/\x60{3}[\s\S]*?\x60{3}/gu, "")
    .replace(/\x60[^\x60\r\n]*\x60/gu, "");
  const escapedBreaks = [...prose.matchAll(/(?:\\r)?\\n/gu)].length;
  const realBreaks = [...prose.matchAll(/\r?\n/gu)].length;
  const escapedStructure =
    /(?:\\r)?\\n(?:[ \t]*(?:#{1,6}[ \t]+|[-*+][ \t]+|\d+[.)][ \t]+)|(?:\\r)?\\n)/u.test(prose);
  if (escapedStructure || (escapedBreaks >= 2 && realBreaks < 2)) {
    return [
      "PR description contains literal escaped newlines (\\n) instead of real Markdown line breaks. " +
      "Write UTF-8 Markdown with actual newlines and use gh pr create/edit --body-file <path>.",
    ];
  }
  return [];
}

function readBody() {
  const fileIndex = process.argv.indexOf("--body-file");
  if (fileIndex !== -1) {
    const path = process.argv[fileIndex + 1];
    if (!path) throw new Error("--body-file requires a path");
    return readFileSync(resolve(path), "utf8");
  }
  if (!process.env.GITHUB_EVENT_PATH) {
    throw new Error("Use --body-file or provide GITHUB_EVENT_PATH with a PR payload.");
  }
  const event = JSON.parse(readFileSync(process.env.GITHUB_EVENT_PATH, "utf8"));
  if (!event.pull_request) return null;
  return event.pull_request.body;
}

if (process.argv[1] && resolve(process.argv[1]) === scriptPath) {
  const body = readBody();
  if (body === null) {
    console.log("Skipping PR body format gate for a non-PR event.");
  } else {
    const violations = pullRequestBodyFormatViolations(body);
    if (violations.length) {
      for (const violation of violations) console.error(violation);
      process.exitCode = 1;
    } else {
      console.log("Pull request Markdown description format: PASS");
    }
  }
}
