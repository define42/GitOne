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

if (!appRoot) {
  throw new Error("missing application root");
}
const app: HTMLElement = appRoot;

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
  const output = element("p", message);
  output.className = error ? "error" : "message";
  output.setAttribute("role", error ? "alert" : "status");
  return output;
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

function copyIcon(): SVGSVGElement {
  const namespace = "http://www.w3.org/2000/svg";
  const icon = document.createElementNS(namespace, "svg");
  icon.setAttribute("viewBox", "0 0 24 24");
  icon.setAttribute("aria-hidden", "true");
  const front = document.createElementNS(namespace, "rect");
  front.setAttribute("x", "8");
  front.setAttribute("y", "8");
  front.setAttribute("width", "12");
  front.setAttribute("height", "13");
  front.setAttribute("rx", "1");
  const back = document.createElementNS(namespace, "path");
  back.setAttribute("d", "M5 16H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1v1");
  icon.append(front, back);
  return icon;
}

function copyButton(value: string): HTMLButtonElement {
  const button = element("button");
  button.type = "button";
  button.className = "copy-button";
  button.title = "Copy repository URL";
  button.setAttribute("aria-label", `Copy ${value}`);
  button.append(copyIcon());
  button.addEventListener("click", async () => {
    try {
      await copyText(value);
      button.classList.add("copied");
      button.title = "Copied";
      button.setAttribute("aria-label", `Copied ${value}`);
      window.setTimeout(() => {
        button.classList.remove("copied");
        button.title = "Copy repository URL";
        button.setAttribute("aria-label", `Copy ${value}`);
      }, 1500);
    } catch (reason) {
      app.prepend(statusMessage(reason instanceof Error ? reason.message : "Could not copy the repository URL.", true));
    }
  });
  return button;
}

function repositoryDeleteControl(groupPath: string, repositoryName: string): HTMLElement {
  const container = element("div");
  container.className = "repository-delete-control";
  const revealButton = element("button", "Delete");
  revealButton.type = "button";
  revealButton.className = "danger";
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
        app.prepend(statusMessage(reason instanceof Error ? reason.message : "Could not delete the repository.", true));
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
  revealButton.className = "danger";
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
        app.prepend(statusMessage(reason instanceof Error ? reason.message : "Could not delete the group.", true));
        confirmButton.disabled = false;
        cancelButton.disabled = false;
      }
    });
  });
  return section;
}

function groupList(groups: GroupSummary[]): HTMLElement {
  if (groups.length === 0) {
    return element("p", "None.");
  }
  const list = element("ul");
  list.className = "group-list";
  for (const group of groups) {
    const item = element("li");
    const link = element("a", group.name);
    link.href = groupURL(group.path);
    item.append(link);
    if (group.description) {
      const description = element("span", group.description);
      description.className = "group-description";
      item.append(description);
    }
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
): HTMLElement {
  const section = element("section");
  section.append(element("h3", heading));
  const form = element("form");
  const label = element("label", labelText);
  const input = element("input");
  input.name = "name";
  input.placeholder = placeholder;
  input.required = true;
  label.append(input);
  const button = element("button", submitText);
  button.type = "submit";
  form.append(label, ...additionalFields, button);
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    button.disabled = true;
    try {
      await onSubmit(input.value);
    } catch (reason) {
      app.prepend(statusMessage(reason instanceof Error ? reason.message : "Request failed.", true));
    } finally {
      button.disabled = false;
    }
  });
  section.append(form);
  return section;
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

function repositoryCommitList(data: RepositoryCommits): HTMLElement {
  const section = element("section");
  section.append(element("h3", "Recent commits"));
  if (data.commits.length === 0) {
    section.append(element("p", "No commits."));
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
      `${commit.author} · ${new Date(commit.committed).toLocaleString()}`,
    );
    item.append(heading, metadata);
    list.append(item);
  }
  section.append(list);
  return section;
}

function repositoryHistory(data: RepositoryCommits): HTMLElement {
  const section = element("section");
  section.className = "repository-history";
  section.append(element("h3", `History for ${data.ref}`));
  if (data.commits.length === 0) {
    section.append(element("p", "No commits."));
    return section;
  }
  section.append(element("p", `Showing the latest ${data.commits.length} commits.`));
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
      `Authored by ${commit.author} <${commit.email}> · ${new Date(commit.authored).toLocaleString()}`,
    );
    const committed = element(
      "span",
      `Committed by ${commit.committer} · ${new Date(commit.committed).toLocaleString()}`,
    );
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
  const files = element("a", "Files");
  files.href = repositoryBrowserURL(route.repository, {ref: route.ref});
  const history = element("a", "History");
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

async function renderRepositoryBrowser(route: RepositoryBrowserRoute): Promise<void> {
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
  const [branches, commits, content] = await Promise.all([
    branchesRequest,
    commitsRequest,
    contentRequest,
  ]);

  document.title = `${route.repository} · GitOne`;
  app.replaceChildren(repositoryBreadcrumbs(route));
  const heading = element("h2", route.repository);
  const ref = element("div");
  ref.className = "repository-ref";
  const branchLabel = element("label");
  branchLabel.append(element("span", "Branch"));
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
      view: route.view,
    });
  });
  branchLabel.append(branchSelect);
  ref.append(branchLabel);
  if (content !== null) {
    const commit = element("p");
    commit.append("Commit: ", element("code", content.commit.slice(0, 12)));
    ref.append(commit);
  }
  app.append(heading, ref, repositoryNavigation(route));

  if (route.view === "history") {
    app.append(repositoryHistory(commits));
    return;
  }
  if (content === null) {
    throw new Error("Repository contents are unavailable.");
  }

  if ("entries" in content) {
    const section = element("section");
    section.append(element("h3", content.path || "Files"));
    if (content.entries.length === 0) {
      section.append(element("p", "This directory is empty."));
    } else {
      const table = element("table");
      table.className = "repository-tree";
      const header = element("tr");
      header.append(element("th", "Name"), element("th", "Type"), element("th", "Size"));
      const head = element("thead");
      head.append(header);
      const body = element("tbody");
      for (const entry of content.entries) {
        const row = element("tr");
        const nameCell = element("td");
        if (entry.type === "directory") {
          const link = element("a", `${entry.name}/`);
          link.href = repositoryBrowserURL(route.repository, {
            ref: route.ref,
            path: entry.path,
          });
          nameCell.append(link);
        } else if (entry.type === "file") {
          const link = element("a", entry.name);
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
          element("td", entry.type),
          element("td", formatFileSize(entry.size)),
        );
        body.append(row);
      }
      table.append(head, body);
      section.append(table);
    }
    app.append(section);
  } else {
    const section = element("section");
    section.append(element("h3", content.path));
    const metadata = element(
      "p",
      `${formatFileSize(content.size)} · ${content.encoding} · ${content.hash.slice(0, 12)}`,
    );
    section.append(metadata);
    if (content.encoding === "utf-8") {
      const pre = element("pre");
      pre.className = "file-content";
      pre.append(element("code", content.content));
      section.append(pre);
    } else {
      section.append(element("p", "Binary file. Content is available as base64 through the API."));
    }
    app.append(section);
  }

  app.append(repositoryCommitList(commits));
}

async function renderRoot(message?: string): Promise<void> {
  const data = await request<GroupList>("/api/groups");
  document.title = "GitOne";
  app.replaceChildren();
  if (message) {
    app.append(statusMessage(message));
  }

  const groups = element("section");
  groups.append(element("h2", "Groups"), groupList(data.groups));
  app.append(groups);

  const description = descriptionField();
  const form = createForm(
    "Create group",
    "Group name",
    "engineering",
    "Create group",
    async (name) => {
      await request(
        `${apiGroupURL(name)}?description=${encodeURIComponent(description.input.value)}`,
        {method: "POST"},
      );
      await renderRoot("Group created.");
    },
    [description.label],
  );
  const explanation = element("p", "The authenticated Basic Auth user becomes the group owner.");
  form.insertBefore(explanation, form.querySelector("form"));
  app.append(form);
}

async function renderGroup(path: string, message?: string): Promise<void> {
  const data = await request<GroupDetail>(apiGroupURL(path));
  document.title = `${data.path} · GitOne`;
  app.replaceChildren();
  if (message) {
    app.append(statusMessage(message));
  }
  app.append(breadcrumbs(data.path), element("h2", data.path));
  if (data.description) {
    const description = element("p", data.description);
    description.className = "group-description";
    app.append(description);
  }

  const subgroups = element("section");
  subgroups.append(element("h3", "Subgroups"), groupList(data.subgroups));
  app.append(subgroups);

  const repositories = element("section");
  repositories.append(element("h3", "Repositories"));
  if (data.repositories.length === 0) {
    repositories.append(element("p", "None."));
  } else {
    const list = element("ul");
    list.className = "repository-list";
    for (const repository of data.repositories) {
      const item = element("li");
      const cloneURL = repositoryURL(data.path, repository.name, data.username);
      const link = element("a", cloneURL);
      link.href = cloneURL;
      link.className = "repository-link";
      const heading = element("div");
      heading.className = "repository-heading";
      const browseLink = element("a", "Browse");
      browseLink.href = repositoryBrowserURL(`${data.path}/${repository.name}`);
      heading.append(link, copyButton(cloneURL), browseLink);
      item.append(heading);
      if (repository.description) {
        const description = element("span", repository.description);
        description.className = "repository-description";
        item.append(description);
      }
      item.append(repositoryDeleteControl(data.path, repository.name));
      list.append(item);
    }
    repositories.append(list);
  }
  app.append(repositories);

  const subgroupDescription = descriptionField();
  app.append(createForm(
    "Create subgroup",
    "Subgroup name",
    "backend",
    "Create subgroup",
    async (name) => {
      await request(
        `${apiGroupURL(`${data.path}/${name}`)}?description=${encodeURIComponent(subgroupDescription.input.value)}`,
        {method: "POST"},
      );
      await renderGroup(data.path, "Subgroup created.");
    },
    [subgroupDescription.label],
  ));

  const initializeReadme = element("input");
  initializeReadme.type = "checkbox";
  initializeReadme.name = "initializeReadme";
  initializeReadme.checked = true;
  const initializeReadmeLabel = element("label");
  initializeReadmeLabel.className = "checkbox-label";
  initializeReadmeLabel.append(initializeReadme, document.createTextNode("Initialize with README.md"));
  const repositoryDescription = descriptionField("What this repository contains");

  app.append(createForm(
    "Create repository",
    "Repository name",
    "api",
    "Create repository",
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
  ));

  app.append(groupDeleteControl(
    data.path,
    data.subgroups.length === 0 && data.repositories.length === 0,
  ));
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
    app.replaceChildren(statusMessage(reason instanceof Error ? reason.message : "Could not load GitOne.", true));
  }
}

void render();
