import {readFile, writeFile} from "node:fs/promises";
import {dirname, join} from "node:path";
import {fileURLToPath} from "node:url";

const directory = dirname(fileURLToPath(import.meta.url));
const prismDirectory = join(directory, "node_modules", "prismjs");
const componentMetadata = JSON.parse(
  await readFile(join(prismDirectory, "components.json"), "utf8"),
);
const requestedLanguages = [
  "bash",
  "c",
  "cpp",
  "csharp",
  "css",
  "dart",
  "diff",
  "docker",
  "elixir",
  "erlang",
  "git",
  "go",
  "go-module",
  "graphql",
  "groovy",
  "hcl",
  "ini",
  "java",
  "javascript",
  "json",
  "json5",
  "jsx",
  "kotlin",
  "lua",
  "makefile",
  "markdown",
  "markup",
  "nginx",
  "objectivec",
  "perl",
  "php",
  "powershell",
  "protobuf",
  "python",
  "r",
  "ruby",
  "rust",
  "scala",
  "sql",
  "swift",
  "toml",
  "tsx",
  "typescript",
  "yaml",
];

const orderedLanguages = [];
const includedLanguages = new Set(["core"]);
function addLanguage(language) {
  if (includedLanguages.has(language)) {
    return;
  }
  const metadata = componentMetadata.languages[language];
  if (!metadata) {
    throw new Error(`Unknown Prism language: ${language}`);
  }
  const dependencies = metadata.require === undefined
    ? []
    : Array.isArray(metadata.require)
      ? metadata.require
      : [metadata.require];
  for (const dependency of dependencies) {
    addLanguage(dependency);
  }
  includedLanguages.add(language);
  orderedLanguages.push(language);
}
for (const language of requestedLanguages) {
  addLanguage(language);
}

const sources = ["core", ...orderedLanguages].map((language) =>
  readFile(
    join(prismDirectory, "components", `prism-${language}.min.js`),
    "utf8",
  )
);
const bundle = (await Promise.all(sources)).join("\n");
await writeFile(
  join(directory, "..", "internal", "webui", "dist", "prism.min.js"),
  `/*! PrismJS 1.30.0 | MIT license | prismjs.com */\n${bundle}\n`,
);
await writeFile(
  join(directory, "..", "internal", "webui", "dist", "prism.LICENSE.txt"),
  await readFile(join(prismDirectory, "LICENSE"), "utf8"),
);
