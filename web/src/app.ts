import DOMPurify from "dompurify";
import {marked} from "marked";

interface GroupSummary {
  name: string;
  path: string;
  description: string;
}

interface GroupList {
  groups: GroupSummary[];
}

interface GroupDetail {
  path: string;
  description: string;
  username: string;
  subgroups: GroupSummary[];
  repositories: RepositorySummary[];
}

interface RepositorySummary {
  name: string;
  description: string;
}

interface RepositoryBranch {
  name: string;
  commit: string;
}

interface RepositoryBranches {
  repository: string;
  defaultBranch: string;
  branches: RepositoryBranch[];
}

interface RepositoryTreeEntry {
  name: string;
  path: string;
  type: "file" | "directory" | "submodule";
  mode: string;
  hash: string;
  size?: number;
}

interface RepositoryTree {
  repository: string;
  ref: string;
  commit: string;
  path: string;
  entries: RepositoryTreeEntry[];
}

interface RepositoryBlob {
  repository: string;
  ref: string;
  commit: string;
  path: string;
  hash: string;
  size: number;
  encoding: "utf-8" | "base64";
  content: string;
}

interface RepositoryCommit {
  hash: string;
  author: string;
  email: string;
  authored: string;
  committer: string;
  committed: string;
  message: string;
}

interface RepositoryCommits {
  repository: string;
  ref: string;
  commits: RepositoryCommit[];
}

interface RepositoryBrowserRoute {
  repository: string;
  ref: string;
  path: string;
  file: string | null;
  view: "files" | "history";
}

interface Problem {
  title?: string;
  detail?: string;
}

const appRoot = document.querySelector<HTMLElement>("#app");
const notificationRoot = document.querySelector<HTMLElement>("#notifications");

if (!appRoot || !notificationRoot) {
  throw new Error("missing application shell");
}
const app: HTMLElement = appRoot;
const notifications: HTMLElement = notificationRoot;

function element<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  text?: string,
): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  if (text !== undefined) {
    node.textContent = text;
  }
  return node;
}

type IconName =
  | "check"
  | "chevron-right"
  | "clock"
  | "close"
  | "copy"
  | "file"
  | "folder"
  | "git-branch"
  | "plus"
  | "repository"
  | "settings"
  | "trash";

const iconPaths: Record<IconName, string[]> = {
  check: ["M20 6 9 17l-5-5"],
  "chevron-right": ["m9 18 6-6-6-6"],
  clock: ["M12 6v6l4 2", "M22 12a10 10 0 1 1-20 0 10 10 0 0 1 20 0Z"],
  close: ["M18 6 6 18", "m6 6 12 12"],
  copy: [
    "M8 8h11a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H10a2 2 0 0 1-2-2Z",
    "M16 8V5a2 2 0 0 0-2-2H5a2 2 0 0 0-2 2v9a2 2 0 0 0 2 2h3",
  ],
  file: ["M14.5 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7.5Z", "M14 2v6h6"],
  folder: ["M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.7-.9l-.8-1.2A2 2 0 0 0 7.9 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z"],
  "git-branch": [
    "M6 3v12",
    "M18 9a9 9 0 0 1-9 9",
    "M9 3a3 3 0 1 1-6 0 3 3 0 0 1 6 0Z",
    "M21 6a3 3 0 1 1-6 0 3 3 0 0 1 6 0Z",
    "M9 18a3 3 0 1 1-6 0 3 3 0 0 1 6 0Z",
  ],
  plus: ["M5 12h14", "M12 5v14"],
  repository: [
    "M15 4h3a2 2 0 0 1 2 2v13a1 1 0 0 1-1 1H6a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h8v18",
    "M8 7h4",
    "M8 11h4",
  ],
  settings: [
    "M12.2 2h-.4a2 2 0 0 0-2 2v.2a2 2 0 0 1-1 1.7l-.4.2a2 2 0 0 1-2 0L6.3 6a2 2 0 0 0-2.7.7l-.2.3a2 2 0 0 0 .7 2.7l.2.1a2 2 0 0 1 1 1.8v.4a2 2 0 0 1-1 1.7l-.2.2a2 2 0 0 0-.7 2.7l.2.3a2 2 0 0 0 2.7.7l.1-.1a2 2 0 0 1 2 0l.4.2a2 2 0 0 1 1 1.7v.2a2 2 0 0 0 2 2h.4a2 2 0 0 0 2-2v-.2a2 2 0 0 1 1-1.7l.4-.2a2 2 0 0 1 2 0l.1.1a2 2 0 0 0 2.7-.7l.2-.3a2 2 0 0 0-.7-2.7l-.2-.2a2 2 0 0 1-1-1.7v-.4a2 2 0 0 1 1-1.8l.2-.1a2 2 0 0 0 .7-2.7l-.2-.3a2 2 0 0 0-2.7-.7l-.1.1a2 2 0 0 1-2 0l-.4-.2a2 2 0 0 1-1-1.7V4a2 2 0 0 0-2-2Z",
    "M15 12a3 3 0 1 1-6 0 3 3 0 0 1 6 0Z",
  ],
  trash: ["M3 6h18", "M8 6V4h8v2", "M19 6l-1 15H6L5 6", "M10 11v5", "M14 11v5"],
};

function icon(name: IconName): SVGSVGElement {
  const namespace = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(namespace, "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("aria-hidden", "true");
  svg.classList.add("icon");
  for (const data of iconPaths[name]) {
    const path = document.createElementNS(namespace, "path");
    path.setAttribute("d", data);
    svg.append(path);
  }
  return svg;
}

function actionButton(
  label: string,
  iconName?: IconName,
  className = "",
): HTMLButtonElement {
  const button = element("button");
  button.type = "button";
  button.className = ["button", className].filter(Boolean).join(" ");
  if (iconName) {
    button.append(icon(iconName));
  }
  button.append(document.createTextNode(label));
  return button;
}

function groupURL(path: string): string {
  return `/groups/${path.split("/").map(encodeURIComponent).join("/")}`;
}

function apiGroupURL(path: string): string {
  return `/api/groups/${encodeURIComponent(path)}`;
}

function repositoryURL(groupPath: string, repository: string, username: string): string {
  const repositoryPath = [
    ...groupPath.split("/"),
    `${repository}.git`,
  ].map(encodeURIComponent).join("/");
  const url = new URL(`/${repositoryPath}`, window.location.origin);
  url.username = username;
  return url.href;
}

function repositoryBrowserURL(
  repository: string,
  options: {
    ref?: string;
    path?: string;
    file?: string;
    view?: "files" | "history";
  } = {},
): string {
  const encodedRepository = repository.split("/").map(encodeURIComponent).join("/");
  const url = new URL(`/repositories/${encodedRepository}`, window.location.origin);
  if (options.ref && options.ref !== "main") {
    url.searchParams.set("ref", options.ref);
  }
  if (options.path) {
    url.searchParams.set("path", options.path);
  }
  if (options.file) {
    url.searchParams.set("file", options.file);
  }
  if (options.view === "history") {
    url.searchParams.set("view", "history");
  }
  return `${url.pathname}${url.search}`;
}

function repositoryBranchesAPIURL(repository: string): string {
  return `/api/repositories/${encodeURIComponent(repository)}/branches`;
}

function repositoryBranchAPIURL(repository: string, branch: string): string {
  return `${repositoryBranchesAPIURL(repository)}/${encodeURIComponent(branch)}`;
}

function repositoryAPIURL(
  repository: string,
  operation: "tree" | "blob" | "commits",
  ref: string,
  path?: string,
): string {
  const base = `/api/repositories/${encodeURIComponent(repository)}/${operation}/${encodeURIComponent(ref)}`;
  return path ? `${base}/${encodeURIComponent(path)}` : base;
}

function currentRepository(): RepositoryBrowserRoute | null {
  const prefix = "/repositories/";
  if (!window.location.pathname.startsWith(prefix)) {
    return null;
  }
  const repository = window.location.pathname
    .slice(prefix.length)
    .split("/")
    .map(decodeURIComponent)
    .join("/");
  if (!repository) {
    return null;
  }
  const parameters = new URLSearchParams(window.location.search);
  return {
    repository,
    ref: parameters.get("ref") || "main",
    path: parameters.get("path") || "",
    file: parameters.get("file"),
    view: parameters.get("view") === "history" ? "history" : "files",
  };
}

function currentGroup(): string | null {
  const prefix = "/groups/";
  if (!window.location.pathname.startsWith(prefix)) {
    return null;
  }
  return window.location.pathname
    .slice(prefix.length)
    .split("/")
    .map(decodeURIComponent)
    .join("/");
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    ...init,
    credentials: "same-origin",
    headers: {
      Accept: "application/json",
      ...init?.headers,
    },
  });
  if (!response.ok) {
    let message = `${response.status} ${response.statusText}`;
    try {
      const problem = await response.json() as Problem;
      message = problem.detail ?? problem.title ?? message;
    } catch {
      // Keep the HTTP status if the response is not JSON.
    }
    throw new Error(message);
  }
  if (response.status === 204) {
    return undefined as T;
  }
  return await response.json() as T;
}

function statusMessage(message: string, error = false): HTMLElement {
  const output = element("div");
  output.className = error ? "toast toast-error" : "toast toast-success";
  output.setAttribute("role", error ? "alert" : "status");
  output.append(icon(error ? "close" : "check"), element("span", message));
  const dismiss = actionButton("Dismiss", "close", "icon-button toast-dismiss");
  dismiss.setAttribute("aria-label", "Dismiss notification");
  dismiss.title = "Dismiss";
  dismiss.addEventListener("click", () => output.remove());
  output.append(dismiss);
  return output;
}

function showStatus(message: string, error = false): void {
  const output = statusMessage(message, error);
  notifications.replaceChildren(output);
  window.setTimeout(() => output.remove(), error ? 9000 : 5000);
}

async function copyText(value: string): Promise<void> {
  if (navigator.clipboard) {
    try {
      await navigator.clipboard.writeText(value);
      return;
    } catch {
      // Fall back for browsers that expose the API but deny clipboard access.
    }
  }

  const input = element("textarea");
  input.value = value;
  input.readOnly = true;
  input.className = "copy-fallback";
  document.body.append(input);
  input.select();
  let copied = false;
  try {
    copied = document.execCommand("copy");
  } finally {
    input.remove();
  }
  if (!copied) {
    throw new Error("Could not copy the repository URL.");
  }
}

function copyButton(value: string): HTMLButtonElement {
  const button = element("button");
  button.type = "button";
  button.className = "icon-button copy-button";
  button.title = "Copy repository URL";
  button.setAttribute("aria-label", `Copy ${value}`);
  button.append(icon("copy"));
  button.addEventListener("click", async () => {
    try {
      await copyText(value);
      button.classList.add("copied");
      button.replaceChildren(icon("check"));
      button.title = "Copied";
      button.setAttribute("aria-label", `Copied ${value}`);
      window.setTimeout(() => {
        button.classList.remove("copied");
        button.replaceChildren(icon("copy"));
        button.title = "Copy repository URL";
        button.setAttribute("aria-label", `Copy ${value}`);
      }, 1500);
    } catch (reason) {
      showStatus(reason instanceof Error ? reason.message : "Could not copy the repository URL.", true);
    }
  });
  return button;
}

function repositoryDeleteControl(groupPath: string, repositoryName: string): HTMLElement {
  const container = element("div");
  container.className = "repository-delete-control";
  const revealButton = element("button", "Delete");
  revealButton.type = "button";
  revealButton.className = "button danger-secondary";
  revealButton.prepend(icon("trash"));
  container.append(revealButton);

  revealButton.addEventListener("click", () => {
    const form = element("form");
    form.className = "repository-delete-form";
    const label = element("label", `Type "${repositoryName}" to confirm deletion`);
    const input = element("input");
    input.name = "repositoryName";
    input.autocomplete = "off";
    input.required = true;
    label.append(input);

    const actions = element("div");
    actions.className = "repository-delete-actions";
    const confirmButton = element("button", "Delete repository");
    confirmButton.type = "submit";
    confirmButton.className = "danger";
    const cancelButton = element("button", "Cancel");
    cancelButton.type = "button";
    actions.append(confirmButton, cancelButton);
    form.append(label, actions);
    container.replaceChildren(form);
    input.focus();

    input.addEventListener("input", () => {
      input.setCustomValidity("");
    });
    cancelButton.addEventListener("click", () => {
      container.replaceChildren(revealButton);
    });
    form.addEventListener("submit", async (event) => {
      event.preventDefault();
      if (input.value !== repositoryName) {
        input.setCustomValidity(`Enter ${repositoryName} exactly.`);
        input.reportValidity();
        return;
      }
      confirmButton.disabled = true;
      cancelButton.disabled = true;
      try {
        const repositoryPath = encodeURIComponent(`${groupPath}/${repositoryName}`);
        await request(`/api/repositories/${repositoryPath}`, {method: "DELETE"});
        await renderGroup(groupPath, `Repository ${repositoryName} deleted.`);
      } catch (reason) {
        showStatus(reason instanceof Error ? reason.message : "Could not delete the repository.", true);
        confirmButton.disabled = false;
        cancelButton.disabled = false;
      }
    });
  });
  return container;
}

function groupDeleteControl(groupPath: string, empty: boolean): HTMLElement {
  const section = element("section");
  section.className = "group-delete-section";
  section.append(element("h3", "Delete group"));
  if (!empty) {
    section.append(element("p", "Remove all repositories and subgroups before deleting this group."));
    return section;
  }

  const container = element("div");
  const revealButton = element("button", "Delete group");
  revealButton.type = "button";
  revealButton.className = "button danger-secondary";
  revealButton.prepend(icon("trash"));
  container.append(revealButton);
  section.append(container);

  revealButton.addEventListener("click", () => {
    const form = element("form");
    form.className = "group-delete-form";
    const label = element("label", `Type "${groupPath}" to confirm deletion`);
    const input = element("input");
    input.name = "groupPath";
    input.autocomplete = "off";
    input.required = true;
    label.append(input);

    const actions = element("div");
    actions.className = "group-delete-actions";
    const confirmButton = element("button", "Delete group");
    confirmButton.type = "submit";
    confirmButton.className = "danger";
    const cancelButton = element("button", "Cancel");
    cancelButton.type = "button";
    actions.append(confirmButton, cancelButton);
    form.append(label, actions);
    container.replaceChildren(form);
    input.focus();

    input.addEventListener("input", () => {
      input.setCustomValidity("");
    });
    cancelButton.addEventListener("click", () => {
      container.replaceChildren(revealButton);
    });
    form.addEventListener("submit", async (event) => {
      event.preventDefault();
      if (input.value !== groupPath) {
        input.setCustomValidity(`Enter ${groupPath} exactly.`);
        input.reportValidity();
        return;
      }
      confirmButton.disabled = true;
      cancelButton.disabled = true;
      try {
        await request(apiGroupURL(groupPath), {method: "DELETE"});
        const parentParts = groupPath.split("/");
        parentParts.pop();
        window.location.assign(parentParts.length > 0 ? groupURL(parentParts.join("/")) : "/");
      } catch (reason) {
        showStatus(reason instanceof Error ? reason.message : "Could not delete the group.", true);
        confirmButton.disabled = false;
        cancelButton.disabled = false;
      }
    });
  });
  return section;
}

function emptyState(message: string): HTMLElement {
  const empty = element("div");
  empty.className = "empty-state";
  empty.append(icon("folder"), element("p", message));
  return empty;
}

function groupList(groups: GroupSummary[], emptyMessage = "No groups yet."): HTMLElement {
  if (groups.length === 0) {
    return emptyState(emptyMessage);
  }
  const list = element("ul");
  list.className = "resource-list group-list";
  for (const group of groups) {
    const item = element("li");
    const link = element("a");
    link.href = groupURL(group.path);
    link.className = "resource-link";
    const iconContainer = element("span");
    iconContainer.className = "resource-icon group-icon";
    iconContainer.append(icon("folder"));
    const content = element("span");
    content.className = "resource-content";
    const name = element("strong", group.name);
    const description = element("span", group.description || "No description");
    description.className = "resource-description";
    content.append(name, description);
    const arrow = icon("chevron-right");
    arrow.classList.add("resource-arrow");
    link.append(iconContainer, content, arrow);
    item.append(link);
    list.append(item);
  }
  return list;
}

function descriptionField(placeholder = "What this group contains"): {label: HTMLLabelElement; input: HTMLInputElement} {
  const label = element("label", "Description");
  const input = element("input");
  input.name = "description";
  input.placeholder = placeholder;
  label.append(input);
  return {label, input};
}

function createForm(
  heading: string,
  labelText: string,
  placeholder: string,
  submitText: string,
  onSubmit: (name: string) => Promise<void>,
  additionalFields: HTMLElement[] = [],
): {trigger: HTMLButtonElement; dialog: HTMLDialogElement} {
  const trigger = actionButton(submitText, "plus", "primary");
  const dialog = element("dialog");
  dialog.className = "action-dialog";
  const form = element("form");
  form.className = "dialog-form";
  const header = element("div");
  header.className = "dialog-header";
  const title = element("h2", heading);
  const close = actionButton("Close", "close", "icon-button");
  close.setAttribute("aria-label", "Close");
  close.title = "Close";
  header.append(title, close);
  const label = element("label", labelText);
  const input = element("input");
  input.name = "name";
  input.placeholder = placeholder;
  input.required = true;
  input.autocomplete = "off";
  label.append(input);
  const actions = element("div");
  actions.className = "dialog-actions";
  const cancel = actionButton("Cancel", undefined, "secondary");
  const button = actionButton(submitText, "plus", "primary");
  button.type = "submit";
  actions.append(cancel, button);
  form.append(header, label, ...additionalFields, actions);
  dialog.append(form);

  trigger.addEventListener("click", () => {
    dialog.showModal();
    input.focus();
  });
  close.addEventListener("click", () => dialog.close());
  cancel.addEventListener("click", () => dialog.close());
  dialog.addEventListener("click", (event) => {
    if (event.target === dialog) {
      dialog.close();
    }
  });
  dialog.addEventListener("close", () => {
    if (trigger.isConnected) {
      trigger.focus();
    }
  });
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    button.disabled = true;
    cancel.disabled = true;
    form.setAttribute("aria-busy", "true");
    try {
      await onSubmit(input.value);
    } catch (reason) {
      showStatus(reason instanceof Error ? reason.message : "Request failed.", true);
    } finally {
      button.disabled = false;
      cancel.disabled = false;
      form.removeAttribute("aria-busy");
    }
  });
  return {trigger, dialog};
}

function pageHeader(
  eyebrow: string,
  title: string,
  description = "",
  actions: HTMLElement[] = [],
): HTMLElement {
  const header = element("section");
  header.className = "page-header";
  const copy = element("div");
  copy.className = "page-header-copy";
  const label = element("span", eyebrow);
  label.className = "eyebrow";
  copy.append(label, element("h1", title));
  if (description) {
    const text = element("p", description);
    text.className = "page-description";
    copy.append(text);
  }
  header.append(copy);
  if (actions.length > 0) {
    const controls = element("div");
    controls.className = "page-actions";
    controls.append(...actions);
    header.append(controls);
  }
  return header;
}

function sectionHeading(
  title: string,
  count?: number,
  actions: HTMLElement[] = [],
): HTMLElement {
  const header = element("div");
  header.className = "section-heading";
  const heading = element("h2", title);
  if (count !== undefined) {
    const badge = element("span", String(count));
    badge.className = "count-badge";
    heading.append(badge);
  }
  header.append(heading, ...actions);
  return header;
}

function breadcrumbs(path: string): HTMLElement {
  const nav = element("nav");
  nav.setAttribute("aria-label", "Breadcrumb");
  const list = element("ol");
  const homeItem = element("li");
  const home = element("a", "Groups");
  home.href = "/";
  homeItem.append(home);
  list.append(homeItem);

  const parts = path.split("/");
  for (let index = 0; index < parts.length; index += 1) {
    const item = element("li");
    const link = element("a", parts[index]);
    link.href = groupURL(parts.slice(0, index + 1).join("/"));
    item.append(link);
    list.append(item);
  }
  nav.append(list);
  return nav;
}

function repositoryBreadcrumbs(route: RepositoryBrowserRoute): HTMLElement {
  const nav = element("nav");
  nav.setAttribute("aria-label", "Breadcrumb");
  const list = element("ol");
  const homeItem = element("li");
  const home = element("a", "Groups");
  home.href = "/";
  homeItem.append(home);
  list.append(homeItem);

  const repositoryParts = route.repository.split("/");
  const groupParts = repositoryParts.slice(0, -1);
  for (let index = 0; index < groupParts.length; index += 1) {
    const item = element("li");
    const link = element("a", groupParts[index]);
    link.href = groupURL(groupParts.slice(0, index + 1).join("/"));
    item.append(link);
    list.append(item);
  }

  const repositoryItem = element("li");
  const repositoryLink = element("a", repositoryParts.at(-1) ?? route.repository);
  repositoryLink.href = repositoryBrowserURL(route.repository, {ref: route.ref});
  repositoryItem.append(repositoryLink);
  list.append(repositoryItem);

  if (route.view === "history") {
    const historyItem = element("li");
    historyItem.append(element("span", "History"));
    list.append(historyItem);
  }

  const selectedPath = route.view === "files" ? route.file ?? route.path : "";
  if (selectedPath) {
    const pathParts = selectedPath.split("/");
    for (let index = 0; index < pathParts.length; index += 1) {
      const item = element("li");
      if (route.file !== null && index === pathParts.length - 1) {
        item.append(element("span", pathParts[index]));
      } else {
        const link = element("a", pathParts[index]);
        link.href = repositoryBrowserURL(route.repository, {
          ref: route.ref,
          path: pathParts.slice(0, index + 1).join("/"),
        });
        item.append(link);
      }
      list.append(item);
    }
  }
  nav.append(list);
  return nav;
}

function formatFileSize(size?: number): string {
  if (size === undefined) {
    return "";
  }
  if (size < 1024) {
    return `${size} B`;
  }
  if (size < 1024 * 1024) {
    return `${(size / 1024).toFixed(1)} KiB`;
  }
  return `${(size / (1024 * 1024)).toFixed(1)} MiB`;
}

function relativeTime(value: string): string {
  const date = new Date(value);
  const seconds = Math.round((date.getTime() - Date.now()) / 1000);
  const formatter = new Intl.RelativeTimeFormat(undefined, {numeric: "auto"});
  const ranges: Array<[Intl.RelativeTimeFormatUnit, number]> = [
    ["year", 60 * 60 * 24 * 365],
    ["month", 60 * 60 * 24 * 30],
    ["week", 60 * 60 * 24 * 7],
    ["day", 60 * 60 * 24],
    ["hour", 60 * 60],
    ["minute", 60],
  ];
  for (const [unit, size] of ranges) {
    if (Math.abs(seconds) >= size) {
      return formatter.format(Math.round(seconds / size), unit);
    }
  }
  return formatter.format(seconds, "second");
}

function repositoryCommitList(data: RepositoryCommits): HTMLElement {
  const section = element("section");
  section.className = "content-section";
  section.append(sectionHeading("Recent commits", data.commits.length));
  if (data.commits.length === 0) {
    section.append(emptyState("No commits yet."));
    return section;
  }
  const list = element("ol");
  list.className = "commit-list";
  for (const commit of data.commits) {
    const item = element("li");
    const heading = element("div");
    const hash = element("code", commit.hash.slice(0, 8));
    const message = element("strong", commit.message.split("\n")[0] || "(no message)");
    heading.append(hash, message);
    const metadata = element(
      "span",
      `${commit.author} committed ${relativeTime(commit.committed)}`,
    );
    metadata.title = new Date(commit.committed).toLocaleString();
    item.append(heading, metadata);
    list.append(item);
  }
  section.append(list);
  return section;
}

function repositoryHistory(data: RepositoryCommits): HTMLElement {
  const section = element("section");
  section.className = "repository-history content-section";
  section.append(sectionHeading(`History for ${data.ref}`, data.commits.length));
  if (data.commits.length === 0) {
    section.append(emptyState("No commits yet."));
    return section;
  }
  const list = element("ol");
  list.className = "history-list";
  for (const commit of data.commits) {
    const item = element("li");
    const heading = element("div");
    heading.className = "history-heading";
    heading.append(
      element("strong", commit.message.split("\n")[0] || "(no message)"),
      element("code", commit.hash),
    );
    const message = element("pre", commit.message.trimEnd() || "(no message)");
    message.className = "commit-message";
    const authored = element(
      "span",
      `Authored by ${commit.author} <${commit.email}> ${relativeTime(commit.authored)}`,
    );
    authored.title = new Date(commit.authored).toLocaleString();
    const committed = element(
      "span",
      `Committed by ${commit.committer} ${relativeTime(commit.committed)}`,
    );
    committed.title = new Date(commit.committed).toLocaleString();
    item.append(heading, message, authored, committed);
    list.append(item);
  }
  section.append(list);
  return section;
}

function repositoryNavigation(route: RepositoryBrowserRoute): HTMLElement {
  const nav = element("nav");
  nav.className = "repository-navigation";
  nav.setAttribute("aria-label", "Repository");
  const files = element("a");
  files.append(icon("repository"), document.createTextNode("Files"));
  files.href = repositoryBrowserURL(route.repository, {ref: route.ref});
  const history = element("a");
  history.append(icon("clock"), document.createTextNode("History"));
  history.href = repositoryBrowserURL(route.repository, {
    ref: route.ref,
    view: "history",
  });
  if (route.view === "history") {
    history.setAttribute("aria-current", "page");
  } else {
    files.setAttribute("aria-current", "page");
  }
  nav.append(files, history);
  return nav;
}

function repositoryBranchCreator(
  route: RepositoryBrowserRoute,
  data: RepositoryBranches,
): {trigger: HTMLButtonElement; dialog: HTMLDialogElement} {
  const trigger = actionButton("New branch", "git-branch", "secondary");
  const dialog = element("dialog");
  dialog.className = "action-dialog";
  if (data.branches.length === 0) {
    trigger.disabled = true;
    trigger.title = "Create a commit before creating another branch";
    return {trigger, dialog};
  }

  const form = element("form");
  form.className = "dialog-form";
  const header = element("div");
  header.className = "dialog-header";
  const title = element("h2", "Create branch");
  const close = actionButton("Close", "close", "icon-button");
  close.setAttribute("aria-label", "Close");
  close.title = "Close";
  header.append(title, close);
  const nameLabel = element("label", "New branch name");
  const name = element("input");
  name.name = "branch";
  name.placeholder = "feature/my-change";
  name.required = true;
  nameLabel.append(name);

  const sourceLabel = element("label", "Create from");
  const source = element("select");
  source.name = "from";
  source.required = true;
  for (const branch of data.branches) {
    const option = element("option", branch.name);
    option.value = branch.name;
    source.append(option);
  }
  const selectedSource = data.branches.some((branch) => branch.name === route.ref)
    ? route.ref
    : data.defaultBranch;
  if (data.branches.some((branch) => branch.name === selectedSource)) {
    source.value = selectedSource;
  }
  sourceLabel.append(source);

  const actions = element("div");
  actions.className = "dialog-actions";
  const cancel = actionButton("Cancel", undefined, "secondary");
  const button = actionButton("Create branch", "git-branch", "primary");
  button.type = "submit";
  actions.append(cancel, button);
  form.append(header, nameLabel, sourceLabel, actions);
  dialog.append(form);

  trigger.addEventListener("click", () => {
    dialog.showModal();
    name.focus();
  });
  close.addEventListener("click", () => dialog.close());
  cancel.addEventListener("click", () => dialog.close());
  dialog.addEventListener("click", (event) => {
    if (event.target === dialog) {
      dialog.close();
    }
  });
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    button.disabled = true;
    cancel.disabled = true;
    form.setAttribute("aria-busy", "true");
    try {
      await request(
        `${repositoryBranchAPIURL(route.repository, name.value)}?from=${encodeURIComponent(source.value)}`,
        {method: "POST"},
      );
      window.location.href = repositoryBrowserURL(route.repository, {
        ref: name.value,
      });
    } catch (reason) {
      showStatus(
        reason instanceof Error ? reason.message : "Could not create the branch.",
        true,
      );
      button.disabled = false;
      cancel.disabled = false;
      form.removeAttribute("aria-busy");
    }
  });
  return {trigger, dialog};
}

function cloneControl(value: string): HTMLElement {
  const control = element("div");
  control.className = "clone-control";
  const label = element("label", "Clone");
  const field = element("div");
  field.className = "clone-field";
  const input = element("input");
  input.value = value;
  input.readOnly = true;
  input.spellcheck = false;
  input.setAttribute("aria-label", "HTTPS clone URL");
  input.addEventListener("focus", () => input.select());
  field.append(input, copyButton(value));
  label.append(field);
  control.append(label);
  return control;
}

function latestCommitBar(data: RepositoryCommits): HTMLElement | null {
  const commit = data.commits[0];
  if (!commit) {
    return null;
  }
  const bar = element("div");
  bar.className = "latest-commit";
  const identity = element("span", commit.author.slice(0, 1).toUpperCase());
  identity.className = "commit-avatar";
  const detail = element("div");
  detail.className = "latest-commit-detail";
  detail.append(
    element("strong", commit.message.split("\n")[0] || "(no message)"),
    element("span", `${commit.author} committed ${relativeTime(commit.committed)}`),
  );
  detail.lastElementChild?.setAttribute("title", new Date(commit.committed).toLocaleString());
  const hash = element("code", commit.hash.slice(0, 8));
  bar.append(identity, detail, hash);
  return bar;
}

async function markdownPreview(content: string): Promise<HTMLElement> {
  const preview = element("article");
  preview.className = "markdown-body";
  const parsed = await marked.parse(content, {gfm: true});
  preview.innerHTML = DOMPurify.sanitize(parsed);
  return preview;
}

function sourcePreview(content: string): HTMLElement {
  const pre = element("pre");
  pre.className = "file-content";
  pre.append(element("code", content));
  return pre;
}

async function repositoryBlobSection(content: RepositoryBlob): Promise<HTMLElement> {
  const section = element("section");
  section.className = "content-section file-view";
  const heading = sectionHeading(content.path);
  const metadata = element(
    "p",
    `${formatFileSize(content.size)} · ${content.encoding} · ${content.hash.slice(0, 12)}`,
  );
  metadata.className = "file-metadata";
  section.append(heading, metadata);

  if (content.encoding !== "utf-8") {
    section.append(emptyState("Binary file. Content is available through the API."));
    return section;
  }

  const isMarkdown = /\.md$/i.test(content.path);
  if (!isMarkdown) {
    section.append(sourcePreview(content.content));
    return section;
  }

  const tabs = element("div");
  tabs.className = "segmented-control";
  tabs.setAttribute("role", "tablist");
  tabs.setAttribute("aria-label", "File view");
  const previewButton = actionButton("Preview");
  const sourceButton = actionButton("Source");
  previewButton.className = "segment active";
  sourceButton.className = "segment";
  previewButton.setAttribute("role", "tab");
  sourceButton.setAttribute("role", "tab");
  previewButton.setAttribute("aria-selected", "true");
  sourceButton.setAttribute("aria-selected", "false");
  const preview = await markdownPreview(content.content);
  const source = sourcePreview(content.content);
  source.hidden = true;
  const select = (showPreview: boolean): void => {
    preview.hidden = !showPreview;
    source.hidden = showPreview;
    previewButton.classList.toggle("active", showPreview);
    sourceButton.classList.toggle("active", !showPreview);
    previewButton.setAttribute("aria-selected", String(showPreview));
    sourceButton.setAttribute("aria-selected", String(!showPreview));
  };
  previewButton.addEventListener("click", () => select(true));
  sourceButton.addEventListener("click", () => select(false));
  tabs.append(previewButton, sourceButton);
  section.append(tabs, preview, source);
  return section;
}

async function repositoryReadme(
  route: RepositoryBrowserRoute,
  content: RepositoryTree,
): Promise<HTMLElement | null> {
  const readme = content.entries.find(
    (entry) => entry.type === "file" && /^readme\.md$/i.test(entry.name),
  );
  if (!readme) {
    return null;
  }
  try {
    const blob = await request<RepositoryBlob>(
      repositoryAPIURL(route.repository, "blob", route.ref, readme.path),
    );
    if (blob.encoding !== "utf-8") {
      return null;
    }
    const section = element("section");
    section.className = "readme-section";
    const header = element("div");
    header.className = "readme-header";
    header.append(icon("repository"), element("strong", readme.name));
    section.append(header, await markdownPreview(blob.content));
    return section;
  } catch {
    return null;
  }
}

async function renderRepositoryBrowser(route: RepositoryBrowserRoute): Promise<void> {
  const repositoryParts = route.repository.split("/");
  const repositoryName = repositoryParts.at(-1) ?? route.repository;
  const groupPath = repositoryParts.slice(0, -1).join("/");
  const branchesRequest = request<RepositoryBranches>(
    repositoryBranchesAPIURL(route.repository),
  );
  const commitsRequest = request<RepositoryCommits>(
    `${repositoryAPIURL(route.repository, "commits", route.ref)}?limit=${route.view === "history" ? 100 : 20}`,
  );
  const contentRequest: Promise<RepositoryTree | RepositoryBlob | null> =
    route.view === "history"
      ? Promise.resolve(null)
      : route.file === null
        ? request<RepositoryTree>(repositoryAPIURL(route.repository, "tree", route.ref, route.path))
        : request<RepositoryBlob>(repositoryAPIURL(route.repository, "blob", route.ref, route.file));
  const groupRequest = request<GroupDetail>(apiGroupURL(groupPath));
  const [branches, commits, content, group] = await Promise.all([
    branchesRequest,
    commitsRequest,
    contentRequest,
    groupRequest,
  ]);
  const repository = group.repositories.find((candidate) => candidate.name === repositoryName);

  document.title = `${route.repository} · GitOne`;
  app.replaceChildren(repositoryBreadcrumbs(route));
  app.append(pageHeader("Repository", route.repository, repository?.description ?? ""));

  const overview = element("section");
  overview.className = "repository-overview";
  overview.append(cloneControl(repositoryURL(groupPath, repositoryName, group.username)));

  const branchCreator = repositoryBranchCreator(route, branches);
  const toolbar = element("div");
  toolbar.className = "repository-toolbar";
  const branchControl = element("div");
  branchControl.className = "branch-control";
  const branchLabel = element("label");
  const branchLabelText = element("span");
  branchLabelText.append(icon("git-branch"), document.createTextNode("Branch"));
  branchLabel.append(branchLabelText);
  const branchSelect = element("select");
  for (const branch of branches.branches) {
    const option = element("option", branch.name);
    option.value = branch.name;
    branchSelect.append(option);
  }
  if (!branches.branches.some((branch) => branch.name === route.ref)) {
    const option = element("option", route.ref);
    option.value = route.ref;
    branchSelect.append(option);
  }
  branchSelect.value = route.ref;
  branchSelect.addEventListener("change", () => {
    window.location.href = repositoryBrowserURL(route.repository, {
      ref: branchSelect.value,
      path: route.file === null ? route.path : undefined,
      file: route.file ?? undefined,
      view: route.view,
    });
  });
  branchLabel.append(branchSelect);
  branchControl.append(branchLabel);
  const commitHash = content?.commit
    ?? branches.branches.find((branch) => branch.name === route.ref)?.commit;
  if (commitHash) {
    const commit = element("div");
    commit.className = "current-commit";
    commit.append(element("span", "Commit"), element("code", commitHash.slice(0, 12)));
    branchControl.append(commit);
  }
  toolbar.append(branchControl, branchCreator.trigger);
  overview.append(toolbar);
  app.append(overview, repositoryNavigation(route), branchCreator.dialog);

  if (route.view === "history") {
    app.append(repositoryHistory(commits));
    return;
  }
  if (content === null) {
    throw new Error("Repository contents are unavailable.");
  }

  if ("entries" in content) {
    const section = element("section");
    section.className = "content-section";
    section.append(sectionHeading(content.path || "Files", content.entries.length));
    const latestCommit = latestCommitBar(commits);
    if (latestCommit) {
      section.append(latestCommit);
    }
    if (content.entries.length === 0) {
      section.append(emptyState("This directory is empty."));
    } else {
      const table = element("table");
      table.className = "repository-tree";
      const header = element("tr");
      header.append(element("th", "Name"), element("th", "Size"));
      const head = element("thead");
      head.append(header);
      const body = element("tbody");
      for (const entry of content.entries) {
        const row = element("tr");
        const nameCell = element("td");
        if (entry.type === "directory") {
          const link = element("a");
          link.append(icon("folder"), document.createTextNode(entry.name));
          link.href = repositoryBrowserURL(route.repository, {
            ref: route.ref,
            path: entry.path,
          });
          nameCell.append(link);
        } else if (entry.type === "file") {
          const link = element("a");
          link.append(icon("file"), document.createTextNode(entry.name));
          link.href = repositoryBrowserURL(route.repository, {
            ref: route.ref,
            file: entry.path,
          });
          nameCell.append(link);
        } else {
          nameCell.append(element("span", entry.name));
        }
        row.append(
          nameCell,
          element("td", formatFileSize(entry.size)),
        );
        body.append(row);
      }
      table.append(head, body);
      section.append(table);
    }
    app.append(section);
    const readme = await repositoryReadme(route, content);
    if (readme) {
      app.append(readme);
    }
  } else {
    app.append(await repositoryBlobSection(content));
  }

  app.append(repositoryCommitList(commits));
}

async function renderRoot(message?: string): Promise<void> {
  const data = await request<GroupList>("/api/groups");
  document.title = "GitOne";
  app.replaceChildren();

  const description = descriptionField();
  const createGroup = createForm(
    "New group",
    "Group name",
    "engineering",
    "New group",
    async (name) => {
      await request(
        `${apiGroupURL(name)}?description=${encodeURIComponent(description.input.value)}`,
        {method: "POST"},
      );
      await renderRoot("Group created.");
    },
    [description.label],
  );

  const groups = element("section");
  groups.className = "content-section";
  groups.append(
    sectionHeading("Your groups", data.groups.length),
    groupList(data.groups),
  );
  app.append(
    pageHeader(
      "Workspace",
      "Groups",
      `${data.groups.length} ${data.groups.length === 1 ? "group" : "groups"} available`,
      [createGroup.trigger],
    ),
    groups,
    createGroup.dialog,
  );
  if (message) {
    showStatus(message);
  }
}

async function renderGroup(path: string, message?: string): Promise<void> {
  const data = await request<GroupDetail>(apiGroupURL(path));
  document.title = `${data.path} · GitOne`;
  app.replaceChildren();

  const subgroupDescription = descriptionField();
  const createSubgroup = createForm(
    "New subgroup",
    "Subgroup name",
    "backend",
    "New subgroup",
    async (name) => {
      await request(
        `${apiGroupURL(`${data.path}/${name}`)}?description=${encodeURIComponent(subgroupDescription.input.value)}`,
        {method: "POST"},
      );
      await renderGroup(data.path, "Subgroup created.");
    },
    [subgroupDescription.label],
  );

  const initializeReadme = element("input");
  initializeReadme.type = "checkbox";
  initializeReadme.name = "initializeReadme";
  initializeReadme.checked = true;
  const initializeReadmeLabel = element("label");
  initializeReadmeLabel.className = "checkbox-label";
  initializeReadmeLabel.append(initializeReadme, document.createTextNode("Initialize with README.md"));
  const repositoryDescription = descriptionField("What this repository contains");

  const createRepository = createForm(
    "New repository",
    "Repository name",
    "api",
    "New repository",
    async (name) => {
      const repositoryPath = encodeURIComponent(`${data.path}/${name}`);
      const parameters = new URLSearchParams({
        initializeReadme: String(initializeReadme.checked),
        description: repositoryDescription.input.value,
      });
      await request(
        `/api/repositories/${repositoryPath}?${parameters}`,
        {method: "POST"},
      );
      await renderGroup(data.path, "Repository created.");
    },
    [repositoryDescription.label, initializeReadmeLabel],
  );

  const subgroups = element("section");
  subgroups.className = "content-section";
  subgroups.append(
    sectionHeading("Subgroups", data.subgroups.length),
    groupList(data.subgroups, "No subgroups yet."),
  );

  const repositories = element("section");
  repositories.className = "content-section";
  repositories.append(sectionHeading("Repositories", data.repositories.length));
  if (data.repositories.length === 0) {
    repositories.append(emptyState("No repositories yet."));
  } else {
    const list = element("ul");
    list.className = "resource-list repository-list";
    for (const repository of data.repositories) {
      const item = element("li");
      const link = element("a");
      link.href = repositoryBrowserURL(`${data.path}/${repository.name}`);
      link.className = "resource-link";
      const iconContainer = element("span");
      iconContainer.className = "resource-icon repository-icon";
      iconContainer.append(icon("repository"));
      const content = element("span");
      content.className = "resource-content";
      content.append(
        element("strong", repository.name),
        element("span", repository.description || "No description"),
      );
      content.lastElementChild?.classList.add("resource-description");
      const arrow = icon("chevron-right");
      arrow.classList.add("resource-arrow");
      link.append(iconContainer, content, arrow);
      item.append(link);
      list.append(item);
    }
    repositories.append(list);
  }

  const settings = element("details");
  settings.className = "settings-panel";
  const settingsSummary = element("summary");
  settingsSummary.append(icon("settings"), document.createTextNode("Group settings"));
  const settingsContent = element("div");
  settingsContent.className = "settings-content";
  if (data.repositories.length > 0) {
    const repositorySettings = element("section");
    repositorySettings.append(element("h3", "Repository deletion"));
    const list = element("ul");
    list.className = "settings-list";
    for (const repository of data.repositories) {
      const item = element("li");
      item.append(
        element("strong", repository.name),
        repositoryDeleteControl(data.path, repository.name),
      );
      list.append(item);
    }
    repositorySettings.append(list);
    settingsContent.append(repositorySettings);
  }
  settingsContent.append(groupDeleteControl(
    data.path,
    data.subgroups.length === 0 && data.repositories.length === 0,
  ));
  settings.append(settingsSummary, settingsContent);

  app.append(
    breadcrumbs(data.path),
    pageHeader(
      "Group",
      data.path,
      data.description,
      [createSubgroup.trigger, createRepository.trigger],
    ),
    subgroups,
    repositories,
    settings,
    createSubgroup.dialog,
    createRepository.dialog,
  );
  if (message) {
    showStatus(message);
  }
}

async function render(): Promise<void> {
  try {
    const repository = currentRepository();
    if (repository !== null) {
      await renderRepositoryBrowser(repository);
      return;
    }
    const group = currentGroup();
    if (group === null) {
      await renderRoot();
    } else {
      await renderGroup(group);
    }
  } catch (reason) {
    const message = reason instanceof Error ? reason.message : "Could not load GitOne.";
    const error = element("section");
    error.className = "load-error";
    error.append(element("h1", "Could not load GitOne"), element("p", message));
    app.replaceChildren(error);
    showStatus(message, true);
  }
}

void render();
