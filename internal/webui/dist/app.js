import DOMPurify from "dompurify";
import { marked } from "marked";
const appRoot = document.querySelector("#app");
const notificationRoot = document.querySelector("#notifications");
if (!appRoot || !notificationRoot) {
    throw new Error("missing application shell");
}
const app = appRoot;
const notifications = notificationRoot;
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
    if (options.view === "history") {
        url.searchParams.set("view", "history");
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
    return {
        repository,
        ref: parameters.get("ref") || "main",
        path: parameters.get("path") || "",
        file: parameters.get("file"),
        view: parameters.get("view") === "history" ? "history" : "files",
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
        throw new Error("Could not copy the repository URL.");
    }
}
function copyButton(value) {
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
        }
        catch (reason) {
            showStatus(reason instanceof Error ? reason.message : "Could not copy the repository URL.", true);
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
async function hashTokenSecret(secret) {
    const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(secret));
    return `sha256:${Array.from(new Uint8Array(digest))
        .map((byte) => byte.toString(16).padStart(2, "0"))
        .join("")}`;
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
        tokenHash.autocomplete = "off";
        tokenHash.spellcheck = false;
        tokenHash.value = token.hash;
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
        fields.append(fieldLabel("Token name", tokenName), fieldLabel("Key", tokenKey), fieldLabel("Role", tokenRole), fieldLabel("Token hash", tokenHash), fieldLabel("New secret", tokenSecret), fieldLabel("Repository scope", tokenRepositories), fieldLabel("Expires", tokenExpiry), disabledLabel);
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
                let hash = row.querySelector(".token-hash")?.value.trim() ?? "";
                if (secret) {
                    hash = await hashTokenSecret(secret);
                }
                if (!tokenName || !key || !hash) {
                    throw new Error("Every token needs a name, key, and hash or new secret.");
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
function repositoryCommitList(data) {
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
        const metadata = element("span", `${commit.author} committed ${relativeTime(commit.committed)}`);
        metadata.title = new Date(commit.committed).toLocaleString();
        item.append(heading, metadata);
        list.append(item);
    }
    section.append(list);
    return section;
}
function repositoryHistory(data) {
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
        heading.append(element("strong", commit.message.split("\n")[0] || "(no message)"), element("code", commit.hash));
        const message = element("pre", commit.message.trimEnd() || "(no message)");
        message.className = "commit-message";
        const authored = element("span", `Authored by ${commit.author} <${commit.email}> ${relativeTime(commit.authored)}`);
        authored.title = new Date(commit.authored).toLocaleString();
        const committed = element("span", `Committed by ${commit.committer} ${relativeTime(commit.committed)}`);
        committed.title = new Date(commit.committed).toLocaleString();
        item.append(heading, message, authored, committed);
        list.append(item);
    }
    section.append(list);
    return section;
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
    if (route.view === "history") {
        history.setAttribute("aria-current", "page");
    }
    else {
        files.setAttribute("aria-current", "page");
    }
    nav.append(files, history);
    return nav;
}
function repositoryBranchCreator(route, data) {
    const trigger = actionButton("New branch", "git-branch", "secondary");
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
    const code = element("code");
    for (const line of file.patch.split("\n")) {
        const row = element("span", line || " ");
        row.className = "diff-line";
        if (line.startsWith("@@")) {
            row.classList.add("diff-hunk");
        }
        else if (line.startsWith("+") && !line.startsWith("+++")) {
            row.classList.add("diff-added");
        }
        else if (line.startsWith("-") && !line.startsWith("---")) {
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
    section.append(pre);
    if (file.truncated) {
        const notice = element("p", "Diff truncated at 1 MiB.");
        notice.className = "diff-truncated";
        section.append(notice);
    }
    return section;
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
                    showStatus(`${comparison.head} ${action} into ${comparison.base} at ${merged.commit.slice(0, 8)}.`);
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
function latestCommitBar(data) {
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
    const hash = element("code", commit.hash.slice(0, 8));
    bar.append(identity, detail, hash);
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
async function repositoryBlobSection(content) {
    const section = element("section");
    section.className = "content-section file-view";
    const heading = sectionHeading(content.path);
    const metadata = element("p", [
        formatFileSize(content.size),
        content.language,
        content.encoding,
        content.hash.slice(0, 12),
    ].filter(Boolean).join(" · "));
    metadata.className = "file-metadata";
    section.append(heading, metadata);
    if (content.encoding !== "utf-8") {
        section.append(emptyState("Binary file. Content is available through the API."));
        return section;
    }
    const isMarkdown = /\.md$/i.test(content.path);
    if (!isMarkdown) {
        section.append(sourcePreview(content.content, content.highlightedHtml));
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
    section.append(tabs, preview, source);
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
    const repositoryParts = route.repository.split("/");
    const repositoryName = repositoryParts.at(-1) ?? route.repository;
    const groupPath = repositoryParts.slice(0, -1).join("/");
    const branchesRequest = request(repositoryBranchesAPIURL(route.repository));
    const commitsRequest = request(`${repositoryAPIURL(route.repository, "commits", route.ref)}?limit=${route.view === "history" ? 100 : 20}`);
    const contentRequest = route.view === "history"
        ? Promise.resolve(null)
        : route.file === null
            ? request(repositoryAPIURL(route.repository, "tree", route.ref, route.path))
            : request(repositoryAPIURL(route.repository, "blob", route.ref, route.file));
    const groupRequest = request(apiGroupURL(groupPath));
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
    const branchComparison = repositoryBranchComparison(route, branches);
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
    const repositoryActions = element("div");
    repositoryActions.className = "repository-actions";
    repositoryActions.append(branchComparison.trigger, branchCreator.trigger);
    toolbar.append(branchControl, repositoryActions);
    overview.append(toolbar);
    app.append(overview, repositoryNavigation(route), branchCreator.dialog, branchComparison.dialog);
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
        app.append(await repositoryBlobSection(content));
    }
    app.append(repositoryCommitList(commits));
}
async function renderRoot(message) {
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
async function render() {
    try {
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
    catch (reason) {
        const message = reason instanceof Error ? reason.message : "Could not load GitOne.";
        const error = element("section");
        error.className = "load-error";
        error.append(element("h1", "Could not load GitOne"), element("p", message));
        app.replaceChildren(error);
        showStatus(message, true);
    }
}
void render();
