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
function createForm(heading, labelText, placeholder, submitText, onSubmit) {
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
    form.append(label, button);
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
        for (const repository of data.repositories) {
            const item = element("li");
            item.append(element("code", `${repository}.git`));
            list.append(item);
        }
        repositories.append(list);
    }
    app.append(repositories);
    app.append(createForm("Create subgroup", "Subgroup name", "backend", "Create subgroup", async (name) => {
        await request(apiGroupURL(`${data.path}/${name}`), { method: "POST" });
        await renderGroup(data.path, "Subgroup created.");
    }));
    app.append(createForm("Create repository", "Repository name", "api", "Create repository", async (name) => {
        const repositoryPath = encodeURIComponent(`${data.path}/${name}`);
        await request(`/api/repositories/${repositoryPath}`, { method: "POST" });
        await renderGroup(data.path, "Repository created.");
    }));
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
