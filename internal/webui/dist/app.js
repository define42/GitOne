import DOMPurify from "dompurify";
import { marked } from "marked";
const appRoot = document.querySelector("#app");
const notificationRoot = document.querySelector("#notifications");
const colorThemeSelect = document.querySelector("#color-theme");
const globalNavigationRoot = document.querySelector("#global-navigation");
const sessionControlsRoot = document.querySelector("#session-controls");
const sessionUsernameRoot = document.querySelector("#session-username");
const logoutRoot = document.querySelector("#logout");
if (!appRoot ||
    !notificationRoot ||
    !colorThemeSelect ||
    !globalNavigationRoot ||
    !sessionControlsRoot ||
    !sessionUsernameRoot ||
    !logoutRoot) {
    throw new Error("missing application shell");
}
const app = appRoot;
const notifications = notificationRoot;
const themeSelect = colorThemeSelect;
const globalNavigation = globalNavigationRoot;
const sessionControls = sessionControlsRoot;
const sessionUsername = sessionUsernameRoot;
const logoutButton = logoutRoot;
let browserSession = null;
let repositoryBuildPollingStop = null;
const colorThemes = [
    "light",
    "dark",
    "steampunk",
    "windows",
    "macosx",
    "ubuntu",
    "solaris",
    "github",
    "gitlab",
];
const colorThemeStorageKey = "gitone-color-theme";
function isColorTheme(value) {
    return colorThemes.includes(value);
}
function applyColorTheme(theme, persist = true) {
    document.documentElement.dataset.theme = theme;
    themeSelect.value = theme;
    if (!persist) {
        return;
    }
    try {
        localStorage.setItem(colorThemeStorageKey, theme);
    }
    catch {
        // The selected theme still applies when browser storage is unavailable.
    }
}
function initializeColorTheme() {
    const initial = isColorTheme(document.documentElement.dataset.theme)
        ? document.documentElement.dataset.theme
        : "dark";
    applyColorTheme(initial, false);
    themeSelect.addEventListener("change", () => {
        if (isColorTheme(themeSelect.value)) {
            applyColorTheme(themeSelect.value);
        }
    });
    window.addEventListener("storage", (event) => {
        const theme = event.newValue ?? undefined;
        if (event.key === colorThemeStorageKey && isColorTheme(theme)) {
            applyColorTheme(theme, false);
        }
    });
}
function element(tag, text) {
    const node = document.createElement(tag);
    if (text !== undefined) {
        node.textContent = text;
    }
    return node;
}
const iconPaths = {
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
    "git-compare": [
        "M18 3v12",
        "m15 12 3 3 3-3",
        "M6 21V9",
        "m3 12-3-3-3 3",
        "m15 6 3-3 3 3",
        "m3 18 3 3 3-3",
    ],
    "git-merge": [
        "M18 18a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z",
        "M6 6a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z",
        "M6 21a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z",
        "M6 9v9",
        "M18 15v-1a5 5 0 0 0-5-5h-2a5 5 0 0 1-5-5",
    ],
    "log-out": [
        "M10 17l5-5-5-5",
        "M15 12H3",
        "M15 3h4a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2h-4",
    ],
    pencil: [
        "M21.2 6.8a1 1 0 0 0-4-4L3.8 16.2a2 2 0 0 0-.5.8L2 21.4a.5.5 0 0 0 .6.6L7 20.7a2 2 0 0 0 .8-.5Z",
        "m15 5 4 4",
    ],
    play: ["m6 3 14 9-14 9Z"],
    plus: ["M5 12h14", "M12 5v14"],
    refresh: [
        "M20 11a8.1 8.1 0 0 0-15.5-2M4 4v5h5",
        "M4 13a8.1 8.1 0 0 0 15.5 2M20 20v-5h-5",
    ],
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
function icon(name) {
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
function actionButton(label, iconName, className = "") {
    const button = element("button");
    button.type = "button";
    button.className = ["button", className].filter(Boolean).join(" ");
    if (iconName) {
        button.append(icon(iconName));
    }
    button.append(document.createTextNode(label));
    return button;
}
function groupURL(path) {
    return `/groups/${path.split("/").map(encodeURIComponent).join("/")}`;
}
function apiGroupURL(path) {
    return `/api/groups/${encodeURIComponent(path)}`;
}
function groupSettingsAPIURL(path) {
    return `${apiGroupURL(path)}/settings`;
}
function repositoryURL(groupPath, repository, username) {
    const repositoryPath = [
        ...groupPath.split("/"),
        `${repository}.git`,
    ].map(encodeURIComponent).join("/");
    const url = new URL(`/${repositoryPath}`, window.location.origin);
    url.username = username;
    return url.href;
}
function repositoryBrowserURL(repository, options = {}) {
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
    if (options.view && options.view !== "files") {
        url.searchParams.set("view", options.view);
    }
    return `${url.pathname}${url.search}`;
}
function repositoryBranchesAPIURL(repository) {
    return `/api/repositories/${encodeURIComponent(repository)}/branches`;
}
function repositoryBranchAPIURL(repository, branch) {
    return `${repositoryBranchesAPIURL(repository)}/${encodeURIComponent(branch)}`;
}
function repositoryComparisonAPIURL(repository, base, head) {
    const parameters = new URLSearchParams({ base, head });
    return `/api/repositories/${encodeURIComponent(repository)}/compare?${parameters}`;
}
function repositoryMergesAPIURL(repository) {
    return `/api/repositories/${encodeURIComponent(repository)}/merges`;
}
function repositoryCommitDiffAPIURL(repository, commit) {
    return `/api/repositories/${encodeURIComponent(repository)}/commits/${encodeURIComponent(commit)}/diff`;
}
function repositoryBuildsAPIURL(repository) {
    return `/api/repositories/${encodeURIComponent(repository)}/builds`;
}
function repositoryBuildAPIURL(repository, id) {
    return `${repositoryBuildsAPIURL(repository)}/${encodeURIComponent(id)}`;
}
function repositoryFileAPIURL(repository, ref, path) {
    return `/api/repositories/${encodeURIComponent(repository)}/files/${encodeURIComponent(ref)}/${encodeURIComponent(path)}`;
}
function repositoryAPIURL(repository, operation, ref, path) {
    const base = `/api/repositories/${encodeURIComponent(repository)}/${operation}/${encodeURIComponent(ref)}`;
    return path ? `${base}/${encodeURIComponent(path)}` : base;
}
function currentRepository() {
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
    const requestedView = parameters.get("view");
    return {
        repository,
        ref: parameters.get("ref") || "main",
        path: parameters.get("path") || "",
        file: parameters.get("file"),
        view: requestedView === "history" || requestedView === "builds"
            ? requestedView
            : "files",
    };
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
class RequestError extends Error {
    status;
    constructor(message, status) {
        super(message);
        this.status = status;
    }
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
        if (response.status === 401 && path !== "/api/session" && browserSession) {
            setBrowserSession(null);
            renderLogin("Your session has expired.");
        }
        throw new RequestError(message, response.status);
    }
    if (response.status === 204) {
        return undefined;
    }
    return await response.json();
}
function statusMessage(message, error = false) {
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
function showStatus(message, error = false) {
    const output = statusMessage(message, error);
    notifications.replaceChildren(output);
    window.setTimeout(() => output.remove(), error ? 9000 : 5000);
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
        throw new Error("Could not copy the clone command.");
    }
}
function copyButton(value) {
    const button = element("button");
    button.type = "button";
    button.className = "icon-button copy-button";
    button.title = "Copy clone command";
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
                button.title = "Copy clone command";
                button.setAttribute("aria-label", `Copy ${value}`);
            }, 1500);
        }
        catch (reason) {
            showStatus(reason instanceof Error ? reason.message : "Could not copy the clone command.", true);
        }
    });
    return button;
}
function repositoryDeleteControl(groupPath, repositoryName) {
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
                await request(`/api/repositories/${repositoryPath}`, { method: "DELETE" });
                await renderGroup(groupPath, `Repository ${repositoryName} deleted.`);
            }
            catch (reason) {
                showStatus(reason instanceof Error ? reason.message : "Could not delete the repository.", true);
                confirmButton.disabled = false;
                cancelButton.disabled = false;
            }
        });
    });
    return container;
}
function groupDeleteControl(groupPath, empty) {
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
                await request(apiGroupURL(groupPath), { method: "DELETE" });
                const parentParts = groupPath.split("/");
                parentParts.pop();
                window.location.assign(parentParts.length > 0 ? groupURL(parentParts.join("/")) : "/");
            }
            catch (reason) {
                showStatus(reason instanceof Error ? reason.message : "Could not delete the group.", true);
                confirmButton.disabled = false;
                cancelButton.disabled = false;
            }
        });
    });
    return section;
}
function emptyState(message) {
    const empty = element("div");
    empty.className = "empty-state";
    empty.append(icon("folder"), element("p", message));
    return empty;
}
function groupList(groups, emptyMessage = "No groups yet.") {
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
function descriptionField(placeholder = "What this group contains") {
    const label = element("label", "Description");
    const input = element("input");
    input.name = "description";
    input.placeholder = placeholder;
    label.append(input);
    return { label, input };
}
function createForm(heading, labelText, placeholder, submitText, onSubmit, additionalFields = []) {
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
        }
        catch (reason) {
            showStatus(reason instanceof Error ? reason.message : "Request failed.", true);
        }
        finally {
            button.disabled = false;
            cancel.disabled = false;
            form.removeAttribute("aria-busy");
        }
    });
    return { trigger, dialog };
}
function pageHeader(eyebrow, title, description = "", actions = []) {
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
function sectionHeading(title, count, actions = []) {
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
function roleSelect(value) {
    const select = element("select");
    for (const role of ["read", "write", "admin", "owner"]) {
        const option = element("option", role[0].toUpperCase() + role.slice(1));
        option.value = role;
        option.selected = role === value;
        select.append(option);
    }
    return select;
}
function fieldLabel(text, field) {
    const label = element("label", text);
    label.append(field);
    return label;
}
function removeButton(label) {
    const button = actionButton("Remove", "trash", "icon-button danger-secondary");
    button.setAttribute("aria-label", label);
    button.title = label;
    return button;
}
function localDateTime(value) {
    if (!value) {
        return "";
    }
    const date = new Date(value);
    const local = new Date(date.getTime() - date.getTimezoneOffset() * 60_000);
    return local.toISOString().slice(0, 16);
}
function groupSettingsControl(path, detail, settings) {
    const trigger = actionButton("Settings", "settings", "secondary");
    const dialog = element("dialog");
    dialog.className = "action-dialog settings-dialog";
    const form = element("form");
    form.className = "settings-form";
    const header = element("div");
    header.className = "dialog-header";
    const title = element("h2", "Group settings");
    const close = actionButton("Close", "close", "icon-button");
    close.setAttribute("aria-label", "Close");
    close.title = "Close";
    header.append(title, close);
    const layout = element("div");
    layout.className = "settings-layout";
    const tabs = element("div");
    tabs.className = "settings-tabs";
    tabs.setAttribute("role", "tablist");
    tabs.setAttribute("aria-label", "Group settings");
    const panels = element("div");
    panels.className = "settings-panels";
    const generalPanel = element("section");
    generalPanel.className = "settings-panel-view";
    generalPanel.setAttribute("role", "tabpanel");
    generalPanel.append(element("h3", "General"));
    const generalGrid = element("div");
    generalGrid.className = "settings-field-grid";
    const name = element("input");
    name.required = true;
    name.autocomplete = "off";
    name.value = settings.group.split("/").at(-1) ?? settings.group;
    const currentPath = element("input");
    currentPath.readOnly = true;
    currentPath.value = settings.group;
    const version = element("input");
    version.readOnly = true;
    version.value = String(settings.version);
    const description = element("textarea");
    description.rows = 4;
    description.value = settings.description;
    const inherit = element("input");
    inherit.type = "checkbox";
    inherit.checked = settings.inherit;
    const inheritLabel = element("label");
    inheritLabel.className = "checkbox-label settings-checkbox";
    inheritLabel.append(inherit, document.createTextNode("Inherit access from the parent group"));
    generalGrid.append(fieldLabel("Group name", name), fieldLabel("Current path", currentPath), fieldLabel("Control version", version), fieldLabel("Description", description), inheritLabel);
    generalPanel.append(generalGrid);
    const accessPanel = element("section");
    accessPanel.className = "settings-panel-view";
    accessPanel.setAttribute("role", "tabpanel");
    const accessHeader = element("div");
    accessHeader.className = "settings-section-header";
    accessHeader.append(element("h3", "Members"));
    const addMember = actionButton("Add member", "plus", "secondary");
    accessHeader.append(addMember);
    const members = element("div");
    members.className = "settings-items member-items";
    const addMemberRow = (username = "", role = "read") => {
        const row = element("fieldset");
        row.className = "settings-item member-row";
        const legend = element("legend", "Member");
        legend.className = "sr-only";
        const memberName = element("input");
        memberName.className = "member-name";
        memberName.required = true;
        memberName.autocomplete = "off";
        memberName.value = username;
        const memberRole = roleSelect(role);
        memberRole.className = "member-role";
        const remove = removeButton(`Remove ${username || "member"}`);
        remove.addEventListener("click", () => row.remove());
        row.append(legend, fieldLabel("Username", memberName), fieldLabel("Role", memberRole), remove);
        members.append(row);
        if (!username) {
            memberName.focus();
        }
    };
    for (const [username, role] of Object.entries(settings.members).sort()) {
        addMemberRow(username, role);
    }
    addMember.addEventListener("click", () => addMemberRow());
    accessPanel.append(accessHeader, members);
    const tokensPanel = element("section");
    tokensPanel.className = "settings-panel-view";
    tokensPanel.setAttribute("role", "tabpanel");
    const tokensHeader = element("div");
    tokensHeader.className = "settings-section-header";
    tokensHeader.append(element("h3", "Tokens"));
    const addToken = actionButton("Add token", "plus", "secondary");
    tokensHeader.append(addToken);
    const tokens = element("div");
    tokens.className = "settings-items token-items";
    const tokenEmpty = element("p", "No tokens.");
    tokenEmpty.className = "settings-empty";
    const refreshTokenEmpty = () => {
        tokenEmpty.hidden = tokens.querySelector(".token-row") !== null;
    };
    const addTokenRow = (token = {
        name: "",
        key: "",
        hash: "",
        role: "write",
    }) => {
        const row = element("fieldset");
        row.className = "settings-item token-row";
        const legend = element("legend", token.name || "New token");
        const remove = removeButton(`Remove ${token.name || "token"}`);
        remove.classList.add("settings-item-remove");
        remove.addEventListener("click", () => {
            row.remove();
            refreshTokenEmpty();
        });
        const fields = element("div");
        fields.className = "settings-field-grid token-field-grid";
        const tokenName = element("input");
        tokenName.className = "token-name";
        tokenName.required = true;
        tokenName.autocomplete = "off";
        tokenName.value = token.name;
        tokenName.addEventListener("input", () => {
            legend.textContent = tokenName.value || "New token";
        });
        const tokenKey = element("input");
        tokenKey.className = "token-key";
        tokenKey.required = true;
        tokenKey.autocomplete = "off";
        tokenKey.value = token.key;
        const tokenRole = roleSelect(token.role);
        tokenRole.className = "token-role";
        const tokenHash = element("input");
        tokenHash.className = "token-hash";
        tokenHash.readOnly = true;
        tokenHash.autocomplete = "off";
        tokenHash.spellcheck = false;
        tokenHash.value = token.hash;
        tokenKey.addEventListener("input", () => {
            tokenHash.value = tokenKey.value.trim() === token.key ? token.hash : "";
        });
        const tokenSecret = element("input");
        tokenSecret.className = "token-secret";
        tokenSecret.type = "password";
        tokenSecret.autocomplete = "new-password";
        const tokenRepositories = element("input");
        tokenRepositories.className = "token-repositories";
        tokenRepositories.placeholder = "api, web";
        tokenRepositories.value = token.repositories?.join(", ") ?? "";
        const tokenExpiry = element("input");
        tokenExpiry.className = "token-expires";
        tokenExpiry.type = "datetime-local";
        tokenExpiry.value = localDateTime(token.expiresAt);
        const tokenDisabled = element("input");
        tokenDisabled.className = "token-disabled";
        tokenDisabled.type = "checkbox";
        tokenDisabled.checked = token.disabled ?? false;
        const disabledLabel = element("label");
        disabledLabel.className = "checkbox-label settings-checkbox";
        disabledLabel.append(tokenDisabled, document.createTextNode("Disabled"));
        fields.append(fieldLabel("Token name", tokenName), fieldLabel("Login key", tokenKey), fieldLabel("Role", tokenRole), fieldLabel("Stored hash", tokenHash), fieldLabel("New secret", tokenSecret), fieldLabel("Repository scope", tokenRepositories), fieldLabel("Expires", tokenExpiry), disabledLabel);
        row.append(legend, remove, fields);
        tokens.append(row);
        refreshTokenEmpty();
        if (!token.name) {
            tokenName.focus();
        }
    };
    for (const token of settings.tokens) {
        addTokenRow(token);
    }
    addToken.addEventListener("click", () => addTokenRow());
    tokensPanel.append(tokensHeader, tokenEmpty, tokens);
    refreshTokenEmpty();
    const repositoriesPanel = element("section");
    repositoriesPanel.className = "settings-panel-view";
    repositoriesPanel.setAttribute("role", "tabpanel");
    const repositoriesHeader = element("div");
    repositoriesHeader.className = "settings-section-header";
    repositoriesHeader.append(element("h3", "Repository policies"));
    const addPolicy = actionButton("Add policy", "plus", "secondary");
    repositoriesHeader.append(addPolicy);
    const repositoryNames = Array.from(new Set([
        ...detail.repositories.map((repository) => repository.name),
        ...Object.keys(settings.repositories),
    ])).sort();
    const repositoryOptions = element("datalist");
    repositoryOptions.id = "repository-policy-options";
    for (const repositoryName of repositoryNames) {
        const option = element("option");
        option.value = repositoryName;
        repositoryOptions.append(option);
    }
    const policies = element("div");
    policies.className = "settings-items policy-items";
    const policyEmpty = element("p", "No repository policies.");
    policyEmpty.className = "settings-empty";
    const refreshPolicyEmpty = () => {
        policyEmpty.hidden = policies.querySelector(".policy-row") !== null;
    };
    const addPolicyRow = (repositoryName = "", policy = {
        visibility: "",
        lfs: { enabled: false },
    }) => {
        const row = element("fieldset");
        row.className = "settings-item policy-row";
        const legend = element("legend", repositoryName || "New policy");
        const remove = removeButton(`Remove ${repositoryName || "policy"}`);
        remove.classList.add("settings-item-remove");
        remove.addEventListener("click", () => {
            row.remove();
            refreshPolicyEmpty();
        });
        const fields = element("div");
        fields.className = "settings-field-grid policy-field-grid";
        const policyRepository = element("input");
        policyRepository.className = "policy-repository";
        policyRepository.required = true;
        policyRepository.autocomplete = "off";
        policyRepository.setAttribute("list", repositoryOptions.id);
        policyRepository.value = repositoryName;
        policyRepository.addEventListener("input", () => {
            legend.textContent = policyRepository.value || "New policy";
        });
        const visibility = element("select");
        visibility.className = "policy-visibility";
        for (const [value, label] of [
            ["", "Default"],
            ["private", "Private"],
            ["internal", "Internal"],
            ["public", "Public"],
        ]) {
            const option = element("option", label);
            option.value = value;
            option.selected = value === (policy.visibility ?? "");
            visibility.append(option);
        }
        const lfsEnabled = element("input");
        lfsEnabled.className = "policy-lfs-enabled";
        lfsEnabled.type = "checkbox";
        lfsEnabled.checked = policy.lfs.enabled;
        const lfsLabel = element("label");
        lfsLabel.className = "checkbox-label settings-checkbox";
        lfsLabel.append(lfsEnabled, document.createTextNode("LFS enabled"));
        const maximumObject = element("input");
        maximumObject.className = "policy-maximum-object";
        maximumObject.type = "number";
        maximumObject.min = "0";
        maximumObject.step = "1";
        maximumObject.value = policy.lfs.maximumObjectBytes
            ? String(policy.lfs.maximumObjectBytes)
            : "";
        const maximumStorage = element("input");
        maximumStorage.className = "policy-maximum-storage";
        maximumStorage.type = "number";
        maximumStorage.min = "0";
        maximumStorage.step = "1";
        maximumStorage.value = policy.lfs.maximumStorageBytes
            ? String(policy.lfs.maximumStorageBytes)
            : "";
        fields.append(fieldLabel("Repository", policyRepository), fieldLabel("Visibility", visibility), lfsLabel, fieldLabel("Maximum object bytes", maximumObject), fieldLabel("Maximum storage bytes", maximumStorage));
        row.append(legend, remove, fields);
        policies.append(row);
        refreshPolicyEmpty();
        if (!repositoryName) {
            policyRepository.focus();
        }
    };
    for (const [repositoryName, policy] of Object.entries(settings.repositories).sort()) {
        addPolicyRow(repositoryName, policy);
    }
    addPolicy.addEventListener("click", () => {
        const used = new Set(Array.from(policies.querySelectorAll(".policy-repository")).map((input) => input.value));
        addPolicyRow(repositoryNames.find((repository) => !used.has(repository)) ?? "");
    });
    repositoriesPanel.append(repositoriesHeader, repositoryOptions, policyEmpty, policies);
    refreshPolicyEmpty();
    const panelDefinitions = [
        ["General", generalPanel],
        ["Access", accessPanel],
        ["Tokens", tokensPanel],
        ["Repositories", repositoriesPanel],
    ];
    const tabButtons = [];
    const selectPanel = (selected) => {
        panelDefinitions.forEach(([, panel], index) => {
            panel.hidden = index !== selected;
            tabButtons[index].classList.toggle("active", index === selected);
            tabButtons[index].setAttribute("aria-selected", String(index === selected));
        });
    };
    panelDefinitions.forEach(([label, panel], index) => {
        const tab = actionButton(label);
        tab.className = "settings-tab";
        tab.setAttribute("role", "tab");
        tab.addEventListener("click", () => selectPanel(index));
        tabButtons.push(tab);
        tabs.append(tab);
        panels.append(panel);
    });
    selectPanel(0);
    layout.append(tabs, panels);
    const actions = element("div");
    actions.className = "dialog-actions";
    const cancel = actionButton("Cancel", undefined, "secondary");
    const save = actionButton("Save changes", undefined, "primary");
    save.type = "submit";
    actions.append(cancel, save);
    form.append(header, layout, actions);
    dialog.append(form);
    trigger.addEventListener("click", () => {
        dialog.showModal();
        selectPanel(0);
        name.focus();
    });
    const discardChanges = () => {
        dialog.close();
        void renderGroup(path);
    };
    close.addEventListener("click", discardChanges);
    cancel.addEventListener("click", discardChanges);
    dialog.addEventListener("click", (event) => {
        if (event.target === dialog) {
            discardChanges();
        }
    });
    dialog.addEventListener("cancel", (event) => {
        event.preventDefault();
        discardChanges();
    });
    dialog.addEventListener("close", () => {
        if (trigger.isConnected) {
            trigger.focus();
        }
    });
    form.addEventListener("submit", async (event) => {
        event.preventDefault();
        save.disabled = true;
        cancel.disabled = true;
        form.setAttribute("aria-busy", "true");
        try {
            const updatedMembers = {};
            for (const row of members.querySelectorAll(".member-row")) {
                const username = row.querySelector(".member-name")?.value.trim() ?? "";
                const role = row.querySelector(".member-role")?.value;
                if (!username) {
                    throw new Error("Every member needs a username.");
                }
                if (username in updatedMembers) {
                    throw new Error(`Member ${username} is listed more than once.`);
                }
                updatedMembers[username] = role;
            }
            const updatedTokens = [];
            for (const row of tokens.querySelectorAll(".token-row")) {
                const tokenName = row.querySelector(".token-name")?.value.trim() ?? "";
                const key = row.querySelector(".token-key")?.value.trim() ?? "";
                const secret = row.querySelector(".token-secret")?.value ?? "";
                const hash = row.querySelector(".token-hash")?.value.trim() ?? "";
                if (!tokenName || !key) {
                    throw new Error("Every token needs a name and key.");
                }
                if (!hash && !secret) {
                    throw new Error("Every new token needs a secret.");
                }
                const repositoryScope = row
                    .querySelector(".token-repositories")
                    ?.value.split(",")
                    .map((value) => value.trim())
                    .filter(Boolean);
                const expiry = row.querySelector(".token-expires")?.value ?? "";
                updatedTokens.push({
                    name: tokenName,
                    key,
                    hash,
                    newSecret: secret || undefined,
                    role: row.querySelector(".token-role")?.value,
                    repositories: repositoryScope,
                    expiresAt: expiry ? new Date(expiry).toISOString() : undefined,
                    disabled: row.querySelector(".token-disabled")?.checked ?? false,
                });
            }
            const updatedPolicies = {};
            for (const row of policies.querySelectorAll(".policy-row")) {
                const repositoryName = row
                    .querySelector(".policy-repository")
                    ?.value.trim() ?? "";
                if (!repositoryName) {
                    throw new Error("Every repository policy needs a repository.");
                }
                if (repositoryName in updatedPolicies) {
                    throw new Error(`Repository ${repositoryName} has more than one policy.`);
                }
                const maximumObjectValue = row
                    .querySelector(".policy-maximum-object")
                    ?.value.trim() ?? "";
                const maximumStorageValue = row
                    .querySelector(".policy-maximum-storage")
                    ?.value.trim() ?? "";
                const maximumObjectBytes = maximumObjectValue ? Number(maximumObjectValue) : 0;
                const maximumStorageBytes = maximumStorageValue ? Number(maximumStorageValue) : 0;
                if (!Number.isSafeInteger(maximumObjectBytes) ||
                    !Number.isSafeInteger(maximumStorageBytes) ||
                    maximumObjectBytes < 0 ||
                    maximumStorageBytes < 0) {
                    throw new Error("Repository limits must be non-negative whole bytes.");
                }
                updatedPolicies[repositoryName] = {
                    visibility: row
                        .querySelector(".policy-visibility")
                        ?.value,
                    lfs: {
                        enabled: row
                            .querySelector(".policy-lfs-enabled")
                            ?.checked ?? false,
                        maximumObjectBytes,
                        maximumStorageBytes,
                    },
                };
            }
            const updated = await request(groupSettingsAPIURL(path), {
                method: "PUT",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({
                    name: name.value.trim(),
                    description: description.value,
                    inherit: inherit.checked,
                    members: updatedMembers,
                    tokens: updatedTokens,
                    repositories: updatedPolicies,
                }),
            });
            dialog.close();
            window.history.replaceState({}, "", groupURL(updated.path));
            await renderGroup(updated.path, "Group settings saved.");
        }
        catch (reason) {
            showStatus(reason instanceof Error ? reason.message : "Could not save group settings.", true);
        }
        finally {
            save.disabled = false;
            cancel.disabled = false;
            form.removeAttribute("aria-busy");
        }
    });
    return { trigger, dialog };
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
function repositoryBreadcrumbs(route) {
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
    repositoryLink.href = repositoryBrowserURL(route.repository, { ref: route.ref });
    repositoryItem.append(repositoryLink);
    list.append(repositoryItem);
    if (route.view === "history") {
        const historyItem = element("li");
        historyItem.append(element("span", "History"));
        list.append(historyItem);
    }
    else if (route.view === "builds") {
        const buildsItem = element("li");
        buildsItem.append(element("span", "Builds"));
        list.append(buildsItem);
    }
    const selectedPath = route.view === "files" ? route.file ?? route.path : "";
    if (selectedPath) {
        const pathParts = selectedPath.split("/");
        for (let index = 0; index < pathParts.length; index += 1) {
            const item = element("li");
            if (route.file !== null && index === pathParts.length - 1) {
                item.append(element("span", pathParts[index]));
            }
            else {
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
function formatFileSize(size) {
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
function shortCommitHash(hash) {
    return hash.slice(0, 12);
}
function relativeTime(value) {
    const date = new Date(value);
    const seconds = Math.round((date.getTime() - Date.now()) / 1000);
    const formatter = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });
    const ranges = [
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
function stopRepositoryBuildPolling() {
    repositoryBuildPollingStop?.();
    repositoryBuildPollingStop = null;
}
function buildDuration(build) {
    if (!build.startedAt) {
        return "Waiting to start";
    }
    const end = build.finishedAt ? new Date(build.finishedAt).getTime() : Date.now();
    const seconds = Math.max(0, Math.floor((end - new Date(build.startedAt).getTime()) / 1000));
    if (seconds < 60) {
        return `${seconds}s`;
    }
    const minutes = Math.floor(seconds / 60);
    if (minutes < 60) {
        return `${minutes}m ${seconds % 60}s`;
    }
    return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}
function buildStatusBadge(status) {
    const badge = element("span");
    badge.className = `build-status build-status-${status}`;
    const statusIcon = status === "succeeded"
        ? "check"
        : status === "failed" ? "close" : "clock";
    const label = status[0].toUpperCase() + status.slice(1);
    badge.append(icon(statusIcon), document.createTextNode(label));
    badge.setAttribute("aria-label", `Build status: ${label}`);
    return badge;
}
function latestBranchBuildIndicator(route, initial) {
    const link = element("a");
    link.className = "latest-build-status";
    link.href = repositoryBrowserURL(route.repository, {
        ref: route.ref,
        view: "builds",
    });
    let data = initial;
    let canceled = false;
    let refreshing = false;
    let timer;
    const latestBuild = () => data.builds.find((build) => build.branch === route.ref);
    const render = () => {
        const label = element("span", "Latest build");
        label.className = "latest-build-label";
        const build = latestBuild();
        if (!build) {
            const badge = element("span");
            badge.className = "build-status build-status-none";
            badge.append(icon("clock"), document.createTextNode("None"));
            link.replaceChildren(label, badge);
            link.title = `No builds have run on ${route.ref}`;
            link.setAttribute("aria-label", `Latest build on ${route.ref}: none`);
            return;
        }
        const badge = buildStatusBadge(build.status);
        link.replaceChildren(label, badge);
        link.title = [
            `Latest build on ${route.ref}: ${build.status}`,
            shortCommitHash(build.commit),
            relativeTime(build.createdAt),
        ].join(" · ");
        link.setAttribute("aria-label", `Latest build on ${route.ref}: ${build.status} at ${shortCommitHash(build.commit)}`);
    };
    const scheduleRefresh = () => {
        if (canceled) {
            return;
        }
        const build = latestBuild();
        const active = build?.status === "queued" || build?.status === "running";
        timer = window.setTimeout(() => void refresh(), document.hidden ? 15_000 : active ? 3_000 : 10_000);
    };
    async function refresh() {
        if (refreshing || canceled) {
            return;
        }
        if (timer !== undefined) {
            window.clearTimeout(timer);
            timer = undefined;
        }
        refreshing = true;
        try {
            data = await request(repositoryBuildsAPIURL(route.repository));
            if (!canceled) {
                render();
            }
        }
        catch {
            if (!canceled) {
                link.title = `Could not refresh the latest build on ${route.ref}`;
            }
        }
        finally {
            refreshing = false;
            scheduleRefresh();
        }
    }
    const visibilityHandler = () => {
        if (!document.hidden) {
            void refresh();
        }
    };
    document.addEventListener("visibilitychange", visibilityHandler);
    repositoryBuildPollingStop = () => {
        canceled = true;
        if (timer !== undefined) {
            window.clearTimeout(timer);
        }
        document.removeEventListener("visibilitychange", visibilityHandler);
    };
    render();
    scheduleRefresh();
    return link;
}
function repositoryBuildsView(route, initial) {
    const section = element("section");
    section.className = "repository-builds content-section";
    const refreshState = element("span", "Updated just now");
    refreshState.className = "build-refresh-state";
    const refreshButton = actionButton("Refresh", "refresh", "secondary build-refresh");
    const heading = sectionHeading("Builds", initial.builds.length, [
        refreshState,
        refreshButton,
    ]);
    const listRoot = element("div");
    listRoot.className = "build-list-root";
    section.append(heading, listRoot);
    let data = initial;
    let canceled = false;
    let refreshing = false;
    let timer;
    const expanded = new Set();
    const logs = new Map();
    const loadingLogs = new Set();
    const renderBuilds = () => {
        const scrollPositions = new Map();
        for (const log of listRoot.querySelectorAll(".build-log")) {
            const id = log.dataset.buildId;
            if (id) {
                scrollPositions.set(id, {
                    top: log.scrollTop,
                    pinned: log.scrollHeight - log.scrollTop - log.clientHeight < 24,
                });
            }
        }
        const count = heading.querySelector(".count-badge");
        if (count) {
            count.textContent = String(data.builds.length);
        }
        if (data.builds.length === 0) {
            listRoot.replaceChildren(emptyState("No builds yet. Push a branch containing a .gitone.json build definition."));
            return;
        }
        const list = element("ol");
        list.className = "build-list";
        for (const build of data.builds) {
            const item = element("li");
            item.className = `build-item build-item-${build.status}`;
            const summary = element("div");
            summary.className = "build-summary";
            const identity = element("div");
            identity.className = "build-identity";
            const title = element("div");
            title.className = "build-title";
            title.append(element("strong", build.branch), element("code", shortCommitHash(build.commit)));
            const metadata = element("div");
            metadata.className = "build-metadata";
            const created = element("span", `Queued ${relativeTime(build.createdAt)}`);
            created.title = new Date(build.createdAt).toLocaleString();
            const duration = element("span", buildDuration(build));
            const image = element("span", build.image ? `Image ${build.image}` : "No image");
            metadata.append(created, duration, image);
            identity.append(title, metadata);
            const controls = element("div");
            controls.className = "build-controls";
            const logButton = actionButton(expanded.has(build.id) ? "Hide log" : "View log", undefined, "secondary build-log-toggle");
            logButton.setAttribute("aria-expanded", String(expanded.has(build.id)));
            logButton.addEventListener("click", () => {
                if (expanded.has(build.id)) {
                    expanded.delete(build.id);
                    renderBuilds();
                    return;
                }
                expanded.add(build.id);
                renderBuilds();
                void loadLog(build.id);
            });
            controls.append(buildStatusBadge(build.status), logButton);
            summary.append(identity, controls);
            item.append(summary);
            if (build.error) {
                const error = element("p", build.error);
                error.className = "build-error";
                error.setAttribute("role", "alert");
                item.append(error);
            }
            if (expanded.has(build.id)) {
                const panel = element("div");
                panel.className = "build-log-panel";
                const logHeader = element("div");
                logHeader.className = "build-log-header";
                const label = element("strong", "Build log");
                const refreshLog = actionButton("Refresh log", "refresh", "secondary");
                refreshLog.disabled = loadingLogs.has(build.id);
                refreshLog.addEventListener("click", () => void loadLog(build.id));
                logHeader.append(label, refreshLog);
                const log = element("pre", loadingLogs.has(build.id)
                    ? "Loading build log…"
                    : logs.get(build.id) ?? "No log output yet.");
                log.className = "build-log";
                log.dataset.buildId = build.id;
                log.tabIndex = 0;
                panel.append(logHeader, log);
                item.append(panel);
            }
            list.append(item);
        }
        listRoot.replaceChildren(list);
        for (const log of listRoot.querySelectorAll(".build-log")) {
            const position = log.dataset.buildId
                ? scrollPositions.get(log.dataset.buildId)
                : undefined;
            if (position) {
                log.scrollTop = position.pinned ? log.scrollHeight : position.top;
            }
        }
    };
    const updateBuild = (updated) => {
        const index = data.builds.findIndex((build) => build.id === updated.id);
        if (index >= 0) {
            data.builds[index] = updated;
        }
    };
    async function loadLog(id) {
        if (loadingLogs.has(id) || canceled) {
            return;
        }
        loadingLogs.add(id);
        renderBuilds();
        try {
            const detail = await request(repositoryBuildAPIURL(route.repository, id));
            if (canceled) {
                return;
            }
            logs.set(id, detail.log || "No log output yet.");
            updateBuild(detail.build);
        }
        catch (reason) {
            if (!canceled) {
                logs.set(id, reason instanceof Error ? `Could not load build log: ${reason.message}` : "Could not load build log.");
            }
        }
        finally {
            loadingLogs.delete(id);
            if (!canceled) {
                renderBuilds();
            }
        }
    }
    const scheduleRefresh = () => {
        if (canceled) {
            return;
        }
        const active = data.builds.some((build) => build.status === "queued" || build.status === "running");
        const delay = document.hidden ? 15_000 : active ? 3_000 : 10_000;
        timer = window.setTimeout(() => void refreshBuilds(), delay);
    };
    async function refreshBuilds() {
        if (refreshing || canceled) {
            return;
        }
        if (timer !== undefined) {
            window.clearTimeout(timer);
            timer = undefined;
        }
        refreshing = true;
        refreshButton.disabled = true;
        refreshState.textContent = "Refreshing…";
        try {
            const previouslyActive = new Set(data.builds.filter((build) => expanded.has(build.id) &&
                (build.status === "queued" || build.status === "running")).map((build) => build.id));
            const refreshed = await request(repositoryBuildsAPIURL(route.repository));
            const liveLogs = refreshed.builds.filter((build) => expanded.has(build.id) &&
                (build.status === "queued" ||
                    build.status === "running" ||
                    previouslyActive.has(build.id) ||
                    !logs.has(build.id)));
            data = refreshed;
            const details = await Promise.all(liveLogs.map(async (build) => {
                try {
                    return await request(repositoryBuildAPIURL(route.repository, build.id));
                }
                catch {
                    return null;
                }
            }));
            for (const detail of details) {
                if (detail) {
                    logs.set(detail.build.id, detail.log || "No log output yet.");
                    updateBuild(detail.build);
                }
            }
            if (!canceled) {
                const updated = new Date();
                refreshState.textContent = "Updated just now";
                refreshState.title = updated.toLocaleString();
                refreshState.removeAttribute("role");
                renderBuilds();
            }
        }
        catch (reason) {
            if (!canceled) {
                refreshState.textContent = reason instanceof Error
                    ? `Refresh failed: ${reason.message}`
                    : "Refresh failed";
                refreshState.setAttribute("role", "alert");
            }
        }
        finally {
            refreshing = false;
            refreshButton.disabled = false;
            scheduleRefresh();
        }
    }
    refreshButton.addEventListener("click", () => void refreshBuilds());
    const visibilityHandler = () => {
        if (!document.hidden) {
            void refreshBuilds();
        }
    };
    document.addEventListener("visibilitychange", visibilityHandler);
    repositoryBuildPollingStop = () => {
        canceled = true;
        if (timer !== undefined) {
            window.clearTimeout(timer);
        }
        document.removeEventListener("visibilitychange", visibilityHandler);
    };
    renderBuilds();
    scheduleRefresh();
    return section;
}
function repositoryHistory(route, data) {
    const section = element("section");
    section.className = "repository-history content-section";
    section.append(sectionHeading(`History for ${data.ref}`, data.total));
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
        const identity = element("div");
        identity.className = "history-identity";
        identity.append(element("strong", commit.message.split("\n")[0] || "(no message)"), element("code", commit.hash));
        const diffButton = actionButton("View diff", "git-compare", "secondary history-diff-toggle");
        diffButton.setAttribute("aria-expanded", "false");
        const diffPanel = element("div");
        diffPanel.className = "history-commit-diff";
        diffPanel.id = `commit-diff-${commit.hash}`;
        diffPanel.hidden = true;
        diffButton.setAttribute("aria-controls", diffPanel.id);
        let diffLoaded = false;
        const setDiffButtonLabel = (label) => {
            diffButton.replaceChildren(icon("git-compare"), document.createTextNode(label));
        };
        diffButton.addEventListener("click", async () => {
            if (!diffPanel.hidden) {
                diffPanel.hidden = true;
                diffButton.setAttribute("aria-expanded", "false");
                setDiffButtonLabel("View diff");
                return;
            }
            diffPanel.hidden = false;
            diffButton.setAttribute("aria-expanded", "true");
            setDiffButtonLabel("Hide diff");
            if (diffLoaded) {
                return;
            }
            diffButton.disabled = true;
            diffPanel.setAttribute("aria-busy", "true");
            diffPanel.replaceChildren(emptyState("Loading commit diff…"));
            try {
                const diff = await request(repositoryCommitDiffAPIURL(route.repository, commit.hash));
                diffPanel.replaceChildren(repositoryCommitDiff(diff));
                diffLoaded = true;
            }
            catch (reason) {
                diffPanel.hidden = true;
                diffButton.setAttribute("aria-expanded", "false");
                setDiffButtonLabel("View diff");
                showStatus(reason instanceof Error ? reason.message : "Could not load the commit diff.", true);
            }
            finally {
                diffPanel.removeAttribute("aria-busy");
                diffButton.disabled = false;
            }
        });
        heading.append(identity, diffButton);
        const message = element("pre", commit.message.trimEnd() || "(no message)");
        message.className = "commit-message";
        const authored = element("span", `Authored by ${commit.author} <${commit.email}> ${relativeTime(commit.authored)}`);
        authored.title = new Date(commit.authored).toLocaleString();
        const committed = element("span", `Committed by ${commit.committer} ${relativeTime(commit.committed)}`);
        committed.title = new Date(commit.committed).toLocaleString();
        item.append(heading, message, authored, committed, diffPanel);
        list.append(item);
    }
    section.append(list);
    return section;
}
function repositoryCommitDiff(data) {
    const content = element("div");
    content.className = "history-diff-content";
    const summary = element("div");
    summary.className = "history-diff-summary";
    const parent = element("span", data.parent
        ? `Compared with parent ${shortCommitHash(data.parent)}`
        : "Initial commit");
    parent.className = "history-diff-parent";
    const stats = element("div");
    stats.className = "comparison-stats";
    const additions = data.files.reduce((total, file) => total + file.additions, 0);
    const deletions = data.files.reduce((total, file) => total + file.deletions, 0);
    stats.append(comparisonStat(data.files.length === 1 ? "file changed" : "files changed", String(data.files.length)), comparisonStat("additions", `+${additions}`), comparisonStat("deletions", `−${deletions}`));
    summary.append(parent, stats);
    content.append(summary);
    const files = element("div");
    files.className = "history-diff-files";
    if (data.files.length === 0) {
        const empty = emptyState("This commit does not change any files.");
        empty.classList.add("history-diff-empty");
        files.append(empty);
    }
    else {
        for (const file of data.files) {
            const fileDiff = comparisonDiff(file);
            fileDiff.classList.add("history-diff-file");
            files.append(fileDiff);
        }
    }
    content.append(files);
    return content;
}
function repositoryNavigation(route) {
    const nav = element("nav");
    nav.className = "repository-navigation";
    nav.setAttribute("aria-label", "Repository");
    const files = element("a");
    files.append(icon("repository"), document.createTextNode("Files"));
    files.href = repositoryBrowserURL(route.repository, { ref: route.ref });
    const history = element("a");
    history.append(icon("clock"), document.createTextNode("History"));
    history.href = repositoryBrowserURL(route.repository, {
        ref: route.ref,
        view: "history",
    });
    const builds = element("a");
    builds.append(icon("play"), document.createTextNode("Builds"));
    builds.href = repositoryBrowserURL(route.repository, {
        ref: route.ref,
        view: "builds",
    });
    if (route.view === "history") {
        history.setAttribute("aria-current", "page");
    }
    else if (route.view === "builds") {
        builds.setAttribute("aria-current", "page");
    }
    else {
        files.setAttribute("aria-current", "page");
    }
    nav.append(files, history, builds);
    return nav;
}
function repositoryBranchCreator(route, data) {
    const trigger = actionButton("New branch", "git-branch", "secondary");
    trigger.classList.add("new-branch-trigger");
    trigger.setAttribute("aria-label", "New branch");
    trigger.title = "New branch";
    const dialog = element("dialog");
    dialog.className = "action-dialog";
    if (data.branches.length === 0) {
        trigger.disabled = true;
        trigger.title = "Create a commit before creating another branch";
        return { trigger, dialog };
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
            await request(`${repositoryBranchAPIURL(route.repository, name.value)}?from=${encodeURIComponent(source.value)}`, { method: "POST" });
            window.location.href = repositoryBrowserURL(route.repository, {
                ref: name.value,
            });
        }
        catch (reason) {
            showStatus(reason instanceof Error ? reason.message : "Could not create the branch.", true);
            button.disabled = false;
            cancel.disabled = false;
            form.removeAttribute("aria-busy");
        }
    });
    return { trigger, dialog };
}
function comparisonStat(label, value) {
    const stat = element("span");
    stat.append(element("strong", value), document.createTextNode(label));
    return stat;
}
function comparisonDiff(file) {
    const section = element("section");
    section.className = "comparison-file";
    const header = element("header");
    const identity = element("div");
    const status = element("span", file.status.slice(0, 1).toUpperCase());
    status.className = `change-status change-${file.status}`;
    const path = element("div");
    path.append(element("strong", file.path));
    if (file.oldPath) {
        path.append(element("span", `from ${file.oldPath}`));
    }
    identity.append(status, path);
    const changes = element("span");
    changes.className = "change-count";
    changes.append(element("span", `+${file.additions}`), element("span", `−${file.deletions}`));
    header.append(identity, changes);
    section.append(header);
    if (file.binary) {
        const binary = element("p", "Binary files differ.");
        binary.className = "binary-diff";
        section.append(binary);
        return section;
    }
    if (!file.patch) {
        return section;
    }
    section.append(diffPatch(file.patch.split("\n")));
    if (file.truncated) {
        const notice = element("p", "Diff truncated at 1 MiB.");
        notice.className = "diff-truncated";
        section.append(notice);
    }
    return section;
}
function diffPatch(lines) {
    const code = element("code");
    let inHunk = false;
    for (const line of lines) {
        const row = element("span", line || " ");
        row.className = "diff-line";
        if (line.startsWith("@@")) {
            inHunk = true;
            row.classList.add("diff-hunk");
        }
        else if (inHunk && line.startsWith("+")) {
            row.classList.add("diff-added");
        }
        else if (inHunk && line.startsWith("-")) {
            row.classList.add("diff-deleted");
        }
        else if (line.startsWith("diff ") ||
            line.startsWith("index ") ||
            line.startsWith("---") ||
            line.startsWith("+++")) {
            row.classList.add("diff-metadata");
        }
        code.append(row);
    }
    const pre = element("pre");
    pre.className = "comparison-patch";
    pre.append(code);
    return pre;
}
function branchComparisonResult(route, comparison, dialog) {
    const result = element("div");
    result.className = "comparison-result";
    const summary = element("section");
    summary.className = "comparison-summary";
    const direction = element("div");
    direction.className = "comparison-direction";
    direction.append(element("code", comparison.base), icon("chevron-right"), element("code", comparison.head));
    const stats = element("div");
    stats.className = "comparison-stats";
    const additions = comparison.files.reduce((total, file) => total + file.additions, 0);
    const deletions = comparison.files.reduce((total, file) => total + file.deletions, 0);
    stats.append(comparisonStat("commits ahead", String(comparison.ahead)), comparisonStat("behind", String(comparison.behind)), comparisonStat("files changed", String(comparison.files.length)), comparisonStat("additions", `+${additions}`), comparisonStat("deletions", `−${deletions}`));
    summary.append(direction, stats);
    result.append(summary);
    const mergeStatus = element("section");
    mergeStatus.className = comparison.mergeable
        ? "merge-status merge-ready"
        : "merge-status merge-conflicted";
    const statusCopy = element("div");
    const statusTitle = element("strong", comparison.mergeable ? "Branches can be merged" : "Merge conflicts detected");
    const statusDetail = element("span", comparison.mergeable
        ? comparison.ahead === 0
            ? `${comparison.head} has no commits to merge into ${comparison.base}.`
            : `${comparison.head} can be merged into ${comparison.base} without conflicts.`
        : "Resolve the conflicting files in a local checkout before merging.");
    statusCopy.append(statusTitle, statusDetail);
    mergeStatus.append(statusCopy);
    if (!comparison.mergeable && comparison.conflicts.length > 0) {
        const conflicts = element("ul");
        conflicts.className = "conflict-list";
        for (const path of comparison.conflicts) {
            conflicts.append(element("li", path));
        }
        mergeStatus.append(conflicts);
    }
    else if (comparison.canMerge && comparison.ahead > 0) {
        const mergeAction = actionButton(`Merge into ${comparison.base}`, "git-merge", "primary");
        const confirmation = element("div");
        confirmation.className = "merge-confirmation";
        mergeAction.addEventListener("click", () => {
            mergeAction.hidden = true;
            const copy = element("span", `Merge ${comparison.head} into ${comparison.base}?`);
            const cancel = actionButton("Cancel", undefined, "secondary");
            const confirm = actionButton("Merge branches", "git-merge", "primary");
            cancel.addEventListener("click", () => {
                confirmation.replaceChildren();
                mergeAction.hidden = false;
            });
            confirm.addEventListener("click", async () => {
                confirm.disabled = true;
                cancel.disabled = true;
                confirmation.setAttribute("aria-busy", "true");
                try {
                    const merged = await request(repositoryMergesAPIURL(route.repository), {
                        method: "POST",
                        headers: { "Content-Type": "application/json" },
                        body: JSON.stringify({
                            target: comparison.base,
                            source: comparison.head,
                        }),
                    });
                    dialog.close();
                    const nextRoute = {
                        repository: route.repository,
                        ref: comparison.base,
                        path: "",
                        file: null,
                        view: "files",
                    };
                    window.history.pushState(null, "", repositoryBrowserURL(route.repository, { ref: comparison.base }));
                    await renderRepositoryBrowser(nextRoute);
                    const action = merged.strategy === "fast-forward"
                        ? "fast-forwarded"
                        : merged.strategy === "already-up-to-date"
                            ? "was already up to date"
                            : "merged";
                    showStatus(`${comparison.head} ${action} into ${comparison.base} at ${shortCommitHash(merged.commit)}.`);
                }
                catch (reason) {
                    showStatus(reason instanceof Error ? reason.message : "Could not merge the branches.", true);
                    confirm.disabled = false;
                    cancel.disabled = false;
                    confirmation.removeAttribute("aria-busy");
                }
            });
            confirmation.append(copy, cancel, confirm);
        });
        mergeStatus.append(mergeAction, confirmation);
    }
    result.append(mergeStatus);
    const files = element("div");
    files.className = "comparison-files";
    if (comparison.files.length === 0) {
        files.append(emptyState("No file changes between these branches."));
    }
    else {
        for (const file of comparison.files) {
            files.append(comparisonDiff(file));
        }
    }
    result.append(files);
    return result;
}
function repositoryBranchComparison(route, data) {
    const trigger = actionButton("Compare", "git-compare", "secondary");
    trigger.classList.add("branch-compare-trigger");
    trigger.setAttribute("aria-label", "Compare branches");
    trigger.title = "Compare branches";
    const dialog = element("dialog");
    dialog.className = "action-dialog comparison-dialog";
    if (data.branches.length < 2) {
        trigger.disabled = true;
        trigger.title = "Create another branch to compare changes";
        return { trigger, dialog };
    }
    const shell = element("div");
    shell.className = "comparison-shell";
    const header = element("div");
    header.className = "dialog-header";
    const title = element("h2", "Compare branches");
    const close = actionButton("Close", "close", "icon-button");
    close.setAttribute("aria-label", "Close");
    close.title = "Close";
    header.append(title, close);
    const form = element("form");
    form.className = "comparison-controls";
    const targetLabel = element("label", "Target");
    const target = element("select");
    target.name = "base";
    const sourceLabel = element("label", "Source");
    const source = element("select");
    source.name = "head";
    for (const branch of data.branches) {
        const targetOption = element("option", branch.name);
        targetOption.value = branch.name;
        target.append(targetOption);
        const sourceOption = element("option", branch.name);
        sourceOption.value = branch.name;
        source.append(sourceOption);
    }
    const selectedTarget = data.branches.some((branch) => branch.name === route.ref)
        ? route.ref
        : data.defaultBranch;
    target.value = selectedTarget;
    const syncSource = () => {
        for (const option of source.options) {
            option.disabled = option.value === target.value;
        }
        if (source.value === target.value || !source.value) {
            source.value = data.branches.find((branch) => branch.name !== target.value)?.name ?? "";
        }
    };
    source.value = data.branches.find((branch) => branch.name !== selectedTarget)?.name ?? "";
    syncSource();
    target.addEventListener("change", syncSource);
    targetLabel.append(target);
    sourceLabel.append(source);
    const compare = actionButton("Compare branches", "git-compare", "primary");
    compare.type = "submit";
    form.append(targetLabel, sourceLabel, compare);
    const body = element("div");
    body.className = "comparison-body";
    body.append(emptyState("Choose a target and source branch."));
    form.addEventListener("submit", async (event) => {
        event.preventDefault();
        compare.disabled = true;
        form.setAttribute("aria-busy", "true");
        const loading = element("p", "Comparing branches…");
        loading.className = "comparison-loading";
        body.replaceChildren(loading);
        try {
            const comparison = await request(repositoryComparisonAPIURL(route.repository, target.value, source.value));
            body.replaceChildren(branchComparisonResult(route, comparison, dialog));
        }
        catch (reason) {
            const error = element("div", reason instanceof Error ? reason.message : "Could not compare the branches.");
            error.className = "comparison-error";
            body.replaceChildren(error);
        }
        finally {
            compare.disabled = false;
            form.removeAttribute("aria-busy");
        }
    });
    shell.append(header, form, body);
    dialog.append(shell);
    trigger.addEventListener("click", () => {
        dialog.showModal();
        void form.requestSubmit();
    });
    close.addEventListener("click", () => dialog.close());
    dialog.addEventListener("click", (event) => {
        if (event.target === dialog) {
            dialog.close();
        }
    });
    return { trigger, dialog };
}
function cloneControl(value) {
    const command = `git clone ${value}`;
    const trigger = actionButton("Clone", "copy", "primary");
    const dialog = element("dialog");
    dialog.className = "action-dialog clone-dialog";
    const content = element("div");
    content.className = "dialog-form";
    const header = element("div");
    header.className = "dialog-header";
    const title = element("h2", "Clone repository");
    title.id = "clone-dialog-title";
    dialog.setAttribute("aria-labelledby", title.id);
    const close = actionButton("Close", "close", "icon-button");
    close.setAttribute("aria-label", "Close");
    close.title = "Close";
    header.append(title, close);
    const label = element("label", "Clone command");
    const field = element("div");
    field.className = "clone-field";
    const input = element("input");
    input.value = command;
    input.readOnly = true;
    input.spellcheck = false;
    input.setAttribute("aria-label", "Git clone command");
    input.addEventListener("focus", () => input.select());
    field.append(input, copyButton(command));
    label.append(field);
    content.append(header, label);
    dialog.append(content);
    trigger.addEventListener("click", () => {
        dialog.showModal();
        input.focus();
    });
    close.addEventListener("click", () => dialog.close());
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
    return { trigger, dialog };
}
function latestCommitBar(data, route, builds) {
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
    detail.append(element("strong", commit.message.split("\n")[0] || "(no message)"), element("span", `${commit.author} committed ${relativeTime(commit.committed)}`));
    detail.lastElementChild?.setAttribute("title", new Date(commit.committed).toLocaleString());
    const hash = element("code", shortCommitHash(commit.hash));
    const metadata = element("div");
    metadata.className = "latest-commit-metadata";
    metadata.append(latestBranchBuildIndicator(route, builds), hash);
    bar.append(identity, detail, metadata);
    return bar;
}
async function markdownPreview(content) {
    const preview = element("article");
    preview.className = "markdown-body";
    const parsed = await marked.parse(content, { gfm: true });
    preview.innerHTML = DOMPurify.sanitize(parsed);
    return preview;
}
function sourcePreview(content, highlightedHtml) {
    if (highlightedHtml) {
        const highlighted = element("div");
        highlighted.className = "file-content highlighted-source";
        highlighted.innerHTML = DOMPurify.sanitize(highlightedHtml, {
            ALLOWED_TAGS: ["pre", "code", "span"],
            ALLOWED_ATTR: ["class", "style"],
        });
        if (highlighted.querySelector("pre")) {
            return highlighted;
        }
    }
    const pre = element("pre");
    pre.className = "file-content";
    pre.append(element("code", content));
    return pre;
}
async function repositoryBlobSection(route, content) {
    const section = element("section");
    section.className = "content-section file-view";
    const editButton = content.canEdit
        ? actionButton("Edit", "pencil", "secondary")
        : null;
    const heading = sectionHeading(content.path, undefined, editButton ? [editButton] : []);
    const metadata = element("p", [
        formatFileSize(content.size),
        content.lfs ? "Git LFS" : undefined,
        content.language,
        content.encoding,
        (content.lfsOid ?? content.hash).slice(0, 12),
    ].filter(Boolean).join(" · "));
    metadata.className = "file-metadata";
    const body = element("div");
    body.className = "file-view-body";
    section.append(heading, metadata, body);
    const isMarkdown = /\.md$/i.test(content.path);
    const renderViewer = async () => {
        if (content.encoding !== "utf-8") {
            body.replaceChildren(emptyState("Binary file. Content is available through the API."));
            return;
        }
        if (!isMarkdown) {
            body.replaceChildren(sourcePreview(content.content, content.highlightedHtml));
            return;
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
        const source = sourcePreview(content.content, content.highlightedHtml);
        source.hidden = true;
        const select = (showPreview) => {
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
        body.replaceChildren(tabs, preview, source);
    };
    editButton?.addEventListener("click", () => {
        const form = element("form");
        form.className = "file-editor";
        const toolbar = element("div");
        toolbar.className = "file-editor-toolbar";
        const tabs = element("div");
        tabs.className = "segmented-control file-editor-tabs";
        tabs.setAttribute("role", "tablist");
        tabs.setAttribute("aria-label", "Editor view");
        const editTab = actionButton("Edit");
        const diffTab = actionButton("Diff");
        editTab.className = "segment active";
        diffTab.className = "segment";
        editTab.setAttribute("role", "tab");
        diffTab.setAttribute("role", "tab");
        editTab.setAttribute("aria-selected", "true");
        diffTab.setAttribute("aria-selected", "false");
        const diffStats = element("span");
        diffStats.className = "change-count file-editor-change-count";
        diffStats.hidden = true;
        tabs.append(editTab, diffTab);
        toolbar.append(tabs, diffStats);
        const textarea = element("textarea");
        textarea.name = "content";
        textarea.value = content.content;
        textarea.spellcheck = false;
        textarea.setAttribute("aria-label", `Contents of ${content.path}`);
        const contentLabel = fieldLabel("File contents", textarea);
        contentLabel.className = "file-editor-content";
        const diffView = element("div");
        diffView.className = "file-editor-diff";
        diffView.hidden = true;
        const message = element("input");
        message.name = "message";
        message.maxLength = 500;
        message.value = `Update ${content.path}`;
        const actions = element("div");
        actions.className = "file-editor-footer";
        const messageLabel = fieldLabel("Commit message", message);
        const buttons = element("div");
        buttons.className = "file-editor-actions";
        const cancel = actionButton("Cancel", undefined, "secondary");
        const save = actionButton("Commit changes", "check", "primary");
        save.type = "submit";
        save.disabled = true;
        buttons.append(cancel, save);
        actions.append(messageLabel, buttons);
        form.append(toolbar, contentLabel, diffView, actions);
        body.replaceChildren(form);
        editButton.hidden = true;
        textarea.focus();
        const updateSaveState = () => {
            save.disabled = textarea.value === content.content;
        };
        let diffGeneration = 0;
        const renderDraftDiff = () => {
            const generation = ++diffGeneration;
            diffStats.hidden = true;
            diffView.replaceChildren(emptyState("Calculating diff…"));
            window.Diff.structuredPatch(content.path, content.path, content.content, textarea.value, "Original", "Working copy", {
                context: 3,
                timeout: 2_000,
                callback: (patch) => {
                    if (generation !== diffGeneration) {
                        return;
                    }
                    if (!patch) {
                        diffView.replaceChildren(emptyState("Diff is too large to display."));
                        return;
                    }
                    if (patch.hunks.length === 0) {
                        diffView.replaceChildren(emptyState("No changes to commit."));
                        return;
                    }
                    const lines = [
                        `--- a/${content.path}`,
                        `+++ b/${content.path}`,
                    ];
                    let additions = 0;
                    let deletions = 0;
                    for (const hunk of patch.hunks) {
                        lines.push(`@@ -${hunk.oldStart},${hunk.oldLines} +${hunk.newStart},${hunk.newLines} @@`, ...hunk.lines);
                        additions += hunk.lines.filter((line) => line.startsWith("+")).length;
                        deletions += hunk.lines.filter((line) => line.startsWith("-")).length;
                    }
                    diffStats.replaceChildren(element("span", `+${additions}`), element("span", `−${deletions}`));
                    diffStats.setAttribute("aria-label", `${additions} additions and ${deletions} deletions`);
                    diffStats.hidden = false;
                    diffView.replaceChildren(diffPatch(lines));
                },
            });
        };
        const selectEditorView = (showEditor) => {
            contentLabel.hidden = !showEditor;
            diffView.hidden = showEditor;
            editTab.classList.toggle("active", showEditor);
            diffTab.classList.toggle("active", !showEditor);
            editTab.setAttribute("aria-selected", String(showEditor));
            diffTab.setAttribute("aria-selected", String(!showEditor));
            if (showEditor) {
                diffStats.hidden = true;
                textarea.focus();
            }
            else {
                renderDraftDiff();
            }
        };
        editTab.addEventListener("click", () => selectEditorView(true));
        diffTab.addEventListener("click", () => selectEditorView(false));
        textarea.addEventListener("input", updateSaveState);
        textarea.addEventListener("keydown", (event) => {
            if (event.key !== "Tab") {
                return;
            }
            event.preventDefault();
            const start = textarea.selectionStart;
            const end = textarea.selectionEnd;
            textarea.setRangeText("\t", start, end, "end");
            updateSaveState();
        });
        cancel.addEventListener("click", async () => {
            diffGeneration++;
            editButton.hidden = false;
            await renderViewer();
            editButton.focus();
        });
        form.addEventListener("submit", async (event) => {
            event.preventDefault();
            save.disabled = true;
            cancel.disabled = true;
            form.setAttribute("aria-busy", "true");
            try {
                const updated = await request(repositoryFileAPIURL(route.repository, route.ref, content.path), {
                    method: "PUT",
                    headers: { "Content-Type": "application/json" },
                    body: JSON.stringify({
                        content: textarea.value,
                        message: message.value,
                        expectedCommit: content.commit,
                    }),
                });
                await renderRepositoryBrowser(route);
                showStatus(`${updated.path} committed to ${updated.branch} at ${shortCommitHash(updated.commit)}.`);
            }
            catch (reason) {
                showStatus(reason instanceof Error ? reason.message : "Could not commit file changes.", true);
                updateSaveState();
                cancel.disabled = false;
                form.removeAttribute("aria-busy");
            }
        });
    });
    await renderViewer();
    return section;
}
async function repositoryReadme(route, content) {
    const readme = content.entries.find((entry) => entry.type === "file" && /^readme\.md$/i.test(entry.name));
    if (!readme) {
        return null;
    }
    try {
        const blob = await request(repositoryAPIURL(route.repository, "blob", route.ref, readme.path));
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
    }
    catch {
        return null;
    }
}
async function renderRepositoryBrowser(route) {
    stopRepositoryBuildPolling();
    const repositoryParts = route.repository.split("/");
    const repositoryName = repositoryParts.at(-1) ?? route.repository;
    const groupPath = repositoryParts.slice(0, -1).join("/");
    const branchesRequest = request(repositoryBranchesAPIURL(route.repository));
    const commitsRequest = request(`${repositoryAPIURL(route.repository, "commits", route.ref)}?limit=${route.view === "history" ? 100 : 1}`);
    const contentRequest = route.view !== "files"
        ? Promise.resolve(null)
        : route.file === null
            ? request(repositoryAPIURL(route.repository, "tree", route.ref, route.path))
            : request(repositoryAPIURL(route.repository, "blob", route.ref, route.file));
    const buildsRequest = route.view === "builds" || (route.view === "files" && route.file === null)
        ? request(repositoryBuildsAPIURL(route.repository))
        : Promise.resolve(null);
    const groupRequest = request(apiGroupURL(groupPath)).catch(() => ({
        path: groupPath,
        description: "",
        username: "",
        subgroups: [],
        repositories: [{ name: repositoryName, description: "" }],
    }));
    const [branches, commits, content, group, builds] = await Promise.all([
        branchesRequest,
        commitsRequest,
        contentRequest,
        groupRequest,
        buildsRequest,
    ]);
    const repository = group.repositories.find((candidate) => candidate.name === repositoryName);
    document.title = `${route.repository} · GitOne`;
    app.replaceChildren(repositoryBreadcrumbs(route));
    app.append(pageHeader("Repository", route.repository, repository?.description ?? ""));
    const overview = element("section");
    overview.className = "repository-overview";
    const branchCreator = repositoryBranchCreator(route, branches);
    const branchComparison = repositoryBranchComparison(route, branches);
    const clone = cloneControl(repositoryURL(groupPath, repositoryName, group.username));
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
    const branchPicker = element("div");
    branchPicker.className = "branch-picker";
    branchPicker.append(branchLabel, branchCreator.trigger, branchComparison.trigger);
    branchControl.append(branchPicker);
    const commitHash = content?.commit
        ?? branches.branches.find((branch) => branch.name === route.ref)?.commit;
    if (commitHash) {
        const commit = element("div");
        commit.className = "current-commit";
        commit.append(element("span", "Commit"), element("code", shortCommitHash(commitHash)));
        const total = element("span", `${commits.total} ${commits.total === 1 ? "commit" : "commits"}`);
        total.className = "current-commit-total";
        commit.append(total);
        branchControl.append(commit);
    }
    const repositoryActions = element("div");
    repositoryActions.className = "repository-actions";
    repositoryActions.append(clone.trigger);
    toolbar.append(branchControl, repositoryActions);
    overview.append(toolbar);
    app.append(overview, repositoryNavigation(route), branchCreator.dialog, branchComparison.dialog, clone.dialog);
    if (route.view === "history") {
        app.append(repositoryHistory(route, commits));
        return;
    }
    if (route.view === "builds") {
        if (builds === null) {
            throw new Error("Repository builds are unavailable.");
        }
        app.append(repositoryBuildsView(route, builds));
        return;
    }
    if (content === null) {
        throw new Error("Repository contents are unavailable.");
    }
    if ("entries" in content) {
        const section = element("section");
        section.className = "content-section";
        if (builds === null) {
            throw new Error("Repository builds are unavailable.");
        }
        const latestCommit = latestCommitBar(commits, route, builds);
        if (latestCommit) {
            section.append(latestCommit);
        }
        if (content.entries.length === 0) {
            section.append(emptyState("This directory is empty."));
        }
        else {
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
                }
                else if (entry.type === "file") {
                    const link = element("a");
                    link.append(icon("file"), document.createTextNode(entry.name));
                    if (entry.lfs) {
                        const badge = element("span", "LFS");
                        badge.className = "lfs-badge";
                        badge.title = "Stored with Git LFS";
                        link.append(badge);
                    }
                    link.href = repositoryBrowserURL(route.repository, {
                        ref: route.ref,
                        file: entry.path,
                    });
                    nameCell.append(link);
                }
                else {
                    nameCell.append(element("span", entry.name));
                }
                row.append(nameCell, element("td", formatFileSize(entry.size)));
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
    }
    else {
        app.append(await repositoryBlobSection(route, content));
    }
}
function setBrowserSession(session) {
    browserSession = session;
    globalNavigation.hidden = session === null;
    sessionControls.hidden = session === null;
    sessionUsername.textContent = session?.username ?? "";
    app.classList.toggle("login-shell", session === null);
}
function renderLogin(message = "") {
    stopRepositoryBuildPolling();
    setBrowserSession(null);
    document.title = "Sign in · GitOne";
    notifications.replaceChildren();
    const view = element("section");
    view.className = "login-view";
    const heading = element("header");
    heading.className = "login-heading";
    const eyebrow = element("span", "GitOne");
    eyebrow.className = "eyebrow";
    heading.append(eyebrow, element("h1", "Sign in"));
    const form = element("form");
    form.className = "login-form";
    const username = element("input");
    username.name = "username";
    username.autocomplete = "username";
    username.required = true;
    username.spellcheck = false;
    const password = element("input");
    password.name = "password";
    password.type = "password";
    password.autocomplete = "current-password";
    password.required = true;
    const error = element("p", message);
    error.className = "login-error";
    error.hidden = message === "";
    error.setAttribute("role", "alert");
    const submit = actionButton("Sign in", undefined, "primary login-submit");
    submit.type = "submit";
    form.append(fieldLabel("Username", username), fieldLabel("Password", password), error, submit);
    view.append(heading, form);
    app.replaceChildren(view);
    username.focus();
    form.addEventListener("submit", async (event) => {
        event.preventDefault();
        submit.disabled = true;
        form.setAttribute("aria-busy", "true");
        error.hidden = true;
        try {
            const session = await request("/api/session", {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({
                    username: username.value.trim(),
                    password: password.value,
                }),
            });
            password.value = "";
            setBrowserSession(session);
            await renderAuthenticated();
        }
        catch (reason) {
            const invalid = reason instanceof RequestError && reason.status === 401;
            error.textContent = invalid
                ? "Invalid username or password."
                : reason instanceof Error ? reason.message : "Could not sign in.";
            error.hidden = false;
            password.select();
        }
        finally {
            submit.disabled = false;
            form.removeAttribute("aria-busy");
        }
    });
}
async function renderRoot(message) {
    stopRepositoryBuildPolling();
    const data = await request("/api/groups");
    document.title = "GitOne";
    app.replaceChildren();
    const description = descriptionField();
    const createGroup = createForm("New group", "Group name", "engineering", "New group", async (name) => {
        await request(`${apiGroupURL(name)}?description=${encodeURIComponent(description.input.value)}`, { method: "POST" });
        await renderRoot("Group created.");
    }, [description.label]);
    const groups = element("section");
    groups.className = "content-section";
    groups.append(sectionHeading("Your groups", data.groups.length), groupList(data.groups));
    app.append(pageHeader("Workspace", "Groups", `${data.groups.length} ${data.groups.length === 1 ? "group" : "groups"} available`, [createGroup.trigger]), groups, createGroup.dialog);
    if (message) {
        showStatus(message);
    }
}
async function renderGroup(path, message) {
    stopRepositoryBuildPolling();
    const [data, controlSettings] = await Promise.all([
        request(apiGroupURL(path)),
        request(groupSettingsAPIURL(path)).catch(() => null),
    ]);
    document.title = `${data.path} · GitOne`;
    app.replaceChildren();
    const subgroupDescription = descriptionField();
    const createSubgroup = createForm("New subgroup", "Subgroup name", "backend", "New subgroup", async (name) => {
        await request(`${apiGroupURL(`${data.path}/${name}`)}?description=${encodeURIComponent(subgroupDescription.input.value)}`, { method: "POST" });
        await renderGroup(data.path, "Subgroup created.");
    }, [subgroupDescription.label]);
    const initializeReadme = element("input");
    initializeReadme.type = "checkbox";
    initializeReadme.name = "initializeReadme";
    initializeReadme.checked = true;
    const initializeReadmeLabel = element("label");
    initializeReadmeLabel.className = "checkbox-label";
    initializeReadmeLabel.append(initializeReadme, document.createTextNode("Initialize with README.md"));
    const repositoryDescription = descriptionField("What this repository contains");
    const createRepository = createForm("New repository", "Repository name", "api", "New repository", async (name) => {
        const repositoryPath = encodeURIComponent(`${data.path}/${name}`);
        const parameters = new URLSearchParams({
            initializeReadme: String(initializeReadme.checked),
            description: repositoryDescription.input.value,
        });
        await request(`/api/repositories/${repositoryPath}?${parameters}`, { method: "POST" });
        await renderGroup(data.path, "Repository created.");
    }, [repositoryDescription.label, initializeReadmeLabel]);
    const settingsControl = controlSettings
        ? groupSettingsControl(data.path, data, controlSettings)
        : null;
    const subgroups = element("section");
    subgroups.className = "content-section";
    subgroups.append(sectionHeading("Subgroups", data.subgroups.length), groupList(data.subgroups, "No subgroups yet."));
    const repositories = element("section");
    repositories.className = "content-section";
    repositories.append(sectionHeading("Repositories", data.repositories.length));
    if (data.repositories.length === 0) {
        repositories.append(emptyState("No repositories yet."));
    }
    else {
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
            content.append(element("strong", repository.name), element("span", repository.description || "No description"));
            content.lastElementChild?.classList.add("resource-description");
            const arrow = icon("chevron-right");
            arrow.classList.add("resource-arrow");
            link.append(iconContainer, content, arrow);
            item.append(link);
            list.append(item);
        }
        repositories.append(list);
    }
    const danger = element("details");
    danger.className = "settings-panel";
    const settingsSummary = element("summary");
    settingsSummary.append(icon("trash"), document.createTextNode("Danger zone"));
    const settingsContent = element("div");
    settingsContent.className = "settings-content";
    if (data.repositories.length > 0) {
        const repositorySettings = element("section");
        repositorySettings.append(element("h3", "Repository deletion"));
        const list = element("ul");
        list.className = "settings-list";
        for (const repository of data.repositories) {
            const item = element("li");
            item.append(element("strong", repository.name), repositoryDeleteControl(data.path, repository.name));
            list.append(item);
        }
        repositorySettings.append(list);
        settingsContent.append(repositorySettings);
    }
    settingsContent.append(groupDeleteControl(data.path, data.subgroups.length === 0 && data.repositories.length === 0));
    danger.append(settingsSummary, settingsContent);
    const pageActions = [
        ...(settingsControl ? [settingsControl.trigger] : []),
        createSubgroup.trigger,
        createRepository.trigger,
    ];
    app.append(breadcrumbs(data.path), pageHeader("Group", data.path, data.description, pageActions), subgroups, repositories, danger, createSubgroup.dialog, createRepository.dialog, ...(settingsControl ? [settingsControl.dialog] : []));
    if (message) {
        showStatus(message);
    }
}
async function renderAuthenticated() {
    const repository = currentRepository();
    if (repository !== null) {
        await renderRepositoryBrowser(repository);
        return;
    }
    const group = currentGroup();
    if (group === null) {
        await renderRoot();
    }
    else {
        await renderGroup(group);
    }
}
async function render() {
    try {
        let session;
        try {
            session = await request("/api/session");
        }
        catch (reason) {
            if (reason instanceof RequestError && reason.status === 401) {
                renderLogin();
                return;
            }
            throw reason;
        }
        setBrowserSession(session);
        await renderAuthenticated();
    }
    catch (reason) {
        if (reason instanceof RequestError && reason.status === 401) {
            renderLogin("Your session has expired.");
            return;
        }
        const message = reason instanceof Error ? reason.message : "Could not load GitOne.";
        const error = element("section");
        error.className = "load-error";
        error.append(element("h1", "Could not load GitOne"), element("p", message));
        app.replaceChildren(error);
        showStatus(message, true);
    }
}
initializeColorTheme();
logoutButton.prepend(icon("log-out"));
logoutButton.addEventListener("click", async () => {
    logoutButton.disabled = true;
    try {
        await request("/api/session", { method: "DELETE" });
    }
    catch (reason) {
        showStatus(reason instanceof Error ? reason.message : "Could not sign out.", true);
    }
    finally {
        logoutButton.disabled = false;
        renderLogin();
    }
});
void render();
