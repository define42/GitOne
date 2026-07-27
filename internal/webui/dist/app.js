"use strict";
const appRoot = document.querySelector("#app");
if (!appRoot) {
    throw new Error("missing application root");
}
const app = appRoot;
function element(tag, text) {
    const node = document.createElement(tag);
    if (text !== undefined) {
        node.textContent = text;
    }
    return node;
}
function groupURL(path) {
    return `/groups/${path.split("/").map(encodeURIComponent).join("/")}`;
}
function apiGroupURL(path) {
    return `/api/groups/${encodeURIComponent(path)}`;
}
function repositoryURL(groupPath, repository) {
    const repositoryPath = [
        ...groupPath.split("/"),
        `${repository}.git`,
    ].map(encodeURIComponent).join("/");
    return new URL(`/${repositoryPath}`, window.location.origin).href;
}
function currentGroup() {
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
async function request(path, init) {
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
            const problem = await response.json();
            message = problem.detail ?? problem.title ?? message;
        }
        catch {
            // Keep the HTTP status if the response is not JSON.
        }
        throw new Error(message);
    }
    if (response.status === 204) {
        return undefined;
    }
    return await response.json();
}
function statusMessage(message, error = false) {
    const output = element("p", message);
    output.className = error ? "error" : "message";
    output.setAttribute("role", error ? "alert" : "status");
    return output;
}
async function copyText(value) {
    if (navigator.clipboard) {
        try {
            await navigator.clipboard.writeText(value);
            return;
        }
        catch {
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
    }
    finally {
        input.remove();
    }
    if (!copied) {
        throw new Error("Could not copy the repository URL.");
    }
}
function copyIcon() {
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
function copyButton(value) {
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
        }
        catch (reason) {
            app.prepend(statusMessage(reason instanceof Error ? reason.message : "Could not copy the repository URL.", true));
        }
    });
    return button;
}
function groupList(groups) {
    if (groups.length === 0) {
        return element("p", "None.");
    }
    const list = element("ul");
    for (const group of groups) {
        const item = element("li");
        const link = element("a", group.name);
        link.href = groupURL(group.path);
        item.append(link);
        list.append(item);
    }
    return list;
}
function createForm(heading, labelText, placeholder, submitText, onSubmit, additionalFields = []) {
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
        }
        catch (reason) {
            app.prepend(statusMessage(reason instanceof Error ? reason.message : "Request failed.", true));
        }
        finally {
            button.disabled = false;
        }
    });
    section.append(form);
    return section;
}
function breadcrumbs(path) {
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
async function renderRoot(message) {
    const data = await request("/api/groups");
    document.title = "GitOne";
    app.replaceChildren();
    if (message) {
        app.append(statusMessage(message));
    }
    const groups = element("section");
    groups.append(element("h2", "Groups"), groupList(data.groups));
    app.append(groups);
    const form = createForm("Create group", "Group name", "engineering", "Create group", async (name) => {
        await request(apiGroupURL(name), { method: "POST" });
        await renderRoot("Group created.");
    });
    const explanation = element("p", "The authenticated Basic Auth user becomes the group owner.");
    form.insertBefore(explanation, form.querySelector("form"));
    app.append(form);
}
async function renderGroup(path, message) {
    const data = await request(apiGroupURL(path));
    document.title = `${data.path} · GitOne`;
    app.replaceChildren();
    if (message) {
        app.append(statusMessage(message));
    }
    app.append(breadcrumbs(data.path), element("h2", data.path));
    const subgroups = element("section");
    subgroups.append(element("h3", "Subgroups"), groupList(data.subgroups));
    app.append(subgroups);
    const repositories = element("section");
    repositories.append(element("h3", "Repositories"));
    if (data.repositories.length === 0) {
        repositories.append(element("p", "None."));
    }
    else {
        const list = element("ul");
        list.className = "repository-list";
        for (const repository of data.repositories) {
            const item = element("li");
            const cloneURL = repositoryURL(data.path, repository);
            const link = element("a", cloneURL);
            link.href = cloneURL;
            link.className = "repository-link";
            item.append(link, copyButton(cloneURL));
            list.append(item);
        }
        repositories.append(list);
    }
    app.append(repositories);
    app.append(createForm("Create subgroup", "Subgroup name", "backend", "Create subgroup", async (name) => {
        await request(apiGroupURL(`${data.path}/${name}`), { method: "POST" });
        await renderGroup(data.path, "Subgroup created.");
    }));
    const initializeReadme = element("input");
    initializeReadme.type = "checkbox";
    initializeReadme.name = "initializeReadme";
    initializeReadme.checked = true;
    const initializeReadmeLabel = element("label");
    initializeReadmeLabel.className = "checkbox-label";
    initializeReadmeLabel.append(initializeReadme, document.createTextNode("Initialize with README.md"));
    app.append(createForm("Create repository", "Repository name", "api", "Create repository", async (name) => {
        const repositoryPath = encodeURIComponent(`${data.path}/${name}`);
        await request(`/api/repositories/${repositoryPath}?initializeReadme=${initializeReadme.checked}`, { method: "POST" });
        await renderGroup(data.path, "Repository created.");
    }, [initializeReadmeLabel]));
}
async function render() {
    try {
        const group = currentGroup();
        if (group === null) {
            await renderRoot();
        }
        else {
            await renderGroup(group);
        }
    }
    catch (reason) {
        app.replaceChildren(statusMessage(reason instanceof Error ? reason.message : "Could not load GitOne.", true));
    }
}
void render();
