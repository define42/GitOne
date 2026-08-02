import DOMPurify from "dompurify";
import { marked } from "marked";
const appRoot = document.querySelector("#app");
const notificationRoot = document.querySelector("#notifications");
const colorThemeSelect = document.querySelector("#color-theme");
const locationContextRoot = document.querySelector("#location-context");
const locationContextListRoot = document.querySelector("#location-context-list");
const globalNavigationRoot = document.querySelector("#global-navigation");
const sessionControlsRoot = document.querySelector("#session-controls");
const sessionUsernameRoot = document.querySelector("#session-username");
const logoutRoot = document.querySelector("#logout");
if (!appRoot ||
    !notificationRoot ||
    !colorThemeSelect ||
    !locationContextRoot ||
    !locationContextListRoot ||
    !globalNavigationRoot ||
    !sessionControlsRoot ||
    !sessionUsernameRoot ||
    !logoutRoot) {
    throw new Error("missing application shell");
}
const app = appRoot;
const notifications = notificationRoot;
const themeSelect = colorThemeSelect;
const locationContext = locationContextRoot;
const locationContextList = locationContextListRoot;
const globalNavigation = globalNavigationRoot;
const sessionControls = sessionControlsRoot;
const sessionUsername = sessionUsernameRoot;
const logoutButton = logoutRoot;
let browserSession = null;
let repositoryBuildPollingStop = null;
let openFileActionMenu = null;
document.addEventListener("pointerdown", (event) => {
    const menu = openFileActionMenu;
    if (menu && (!menu.isConnected || !menu.contains(event.target))) {
        menu.open = false;
        openFileActionMenu = null;
    }
});
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
    download: ["M12 3v12", "m7 10 5 5 5-5", "M5 21h14"],
    ellipsis: [
        "M6 13a1 1 0 1 0 0-2 1 1 0 0 0 0 2Z",
        "M12 13a1 1 0 1 0 0-2 1 1 0 0 0 0 2Z",
        "M18 13a1 1 0 1 0 0-2 1 1 0 0 0 0 2Z",
    ],
    file: ["M14.5 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7.5Z", "M14 2v6h6"],
    folder: ["M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.7-.9l-.8-1.2A2 2 0 0 0 7.9 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z"],
    group: [
        "M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2",
        "M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8Z",
        "M22 21v-2a4 4 0 0 0-3-3.9",
        "M16 3.1a4 4 0 0 1 0 7.8",
    ],
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
    home: [
        "M3 11 12 4l9 7",
        "M5 10v10h14V10",
        "M9 20v-6h6v6",
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
    if (options.ref) {
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
    if (options.page && options.page > 1) {
        url.searchParams.set("page", String(options.page));
    }
    if (options.mergeRequest !== undefined) {
        url.searchParams.set("request", String(options.mergeRequest));
    }
    if (options.reviewTab) {
        url.searchParams.set("tab", options.reviewTab);
    }
    if (options.mergeRequestState) {
        url.searchParams.set("state", options.mergeRequestState);
    }
    return `${url.pathname}${url.search}`;
}
const locationContextIcons = {
    root: "home",
    group: "group",
    subgroup: "group",
    repository: "repository",
    folder: "folder",
    file: "file",
};
const rootLocationContextItem = {
    label: "Root",
    href: "/",
    kind: "root",
};
function setLocationContext(items) {
    const entries = items.map((item, index) => {
        const entry = element("li");
        entry.className = `location-${item.kind}`;
        const link = element("a");
        link.href = item.href;
        link.title = item.kind === "root"
            ? "GitOne root"
            : `${item.kind[0].toUpperCase()}${item.kind.slice(1)}: ${item.label}`;
        link.append(icon(locationContextIcons[item.kind]), document.createTextNode(item.label));
        if (index === items.length - 1) {
            link.setAttribute("aria-current", "page");
        }
        entry.append(link);
        return entry;
    });
    locationContextList.replaceChildren(...entries);
    locationContext.hidden = entries.length === 0;
}
function setGroupLocation(path) {
    const parts = path.split("/");
    setLocationContext([
        rootLocationContextItem,
        ...parts.map((part, index) => ({
            label: part,
            href: groupURL(parts.slice(0, index + 1).join("/")),
            kind: index === 0 ? "group" : "subgroup",
        })),
    ]);
}
function setRepositoryLocation(route) {
    const parts = route.repository.split("/");
    const groupParts = parts.slice(0, -1);
    const selectedPath = route.view === "files" || route.view === "blame"
        ? route.file ?? route.path
        : "";
    const pathParts = selectedPath ? selectedPath.split("/") : [];
    setLocationContext([
        rootLocationContextItem,
        ...groupParts.map((part, index) => ({
            label: part,
            href: groupURL(groupParts.slice(0, index + 1).join("/")),
            kind: index === 0 ? "group" : "subgroup",
        })),
        {
            label: parts.at(-1) ?? route.repository,
            href: repositoryBrowserURL(route.repository, { ref: route.ref }),
            kind: "repository",
        },
        ...pathParts.map((part, index) => {
            const path = pathParts.slice(0, index + 1).join("/");
            const isFile = route.file !== null && index === pathParts.length - 1;
            return {
                label: part,
                kind: isFile ? "file" : "folder",
                href: isFile
                    ? repositoryBrowserURL(route.repository, {
                        ref: route.ref,
                        file: path,
                        view: route.view === "blame" ? "blame" : undefined,
                    })
                    : repositoryBrowserURL(route.repository, { ref: route.ref, path }),
            };
        }),
    ]);
}
function repositoryBranchesAPIURL(repository) {
    return `/api/repositories/${encodeURIComponent(repository)}/branches`;
}
function repositoryDefaultBranchAPIURL(repository) {
    return `/api/repositories/${encodeURIComponent(repository)}/default-branch`;
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
function repositoryMergeRequestsAPIURL(repository, mergeRequest) {
    const collection = `/api/repositories/${encodeURIComponent(repository)}/merge-requests`;
    return mergeRequest === undefined
        ? collection
        : `${collection}/${encodeURIComponent(String(mergeRequest))}`;
}
function repositoryMergeRequestThreadsAPIURL(repository, mergeRequest, thread) {
    const collection = `${repositoryMergeRequestsAPIURL(repository, mergeRequest)}/threads`;
    return thread === undefined
        ? collection
        : `${collection}/${encodeURIComponent(String(thread))}`;
}
function repositoryMergeRequestCommentsAPIURL(repository, mergeRequest, thread) {
    return `${repositoryMergeRequestThreadsAPIURL(repository, mergeRequest, thread)}/comments`;
}
function repositoryMergeRequestApprovalsAPIURL(repository, mergeRequest) {
    return `${repositoryMergeRequestsAPIURL(repository, mergeRequest)}/approvals`;
}
function repositoryMergeRequestMergeAPIURL(repository, mergeRequest) {
    return `${repositoryMergeRequestsAPIURL(repository, mergeRequest)}/merge`;
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
function repositoryBuildActionAPIURL(repository, id, action) {
    return `${repositoryBuildAPIURL(repository, id)}/${action}`;
}
function repositoryFileAPIURL(repository, ref, path) {
    return `/api/repositories/${encodeURIComponent(repository)}/files/${encodeURIComponent(ref)}/${encodeURIComponent(path)}`;
}
function repositoryArchiveAPIURL(repository, ref, format) {
    const parameters = new URLSearchParams({ format });
    return `/api/repositories/${encodeURIComponent(repository)}/archives/${encodeURIComponent(ref)}?${parameters}`;
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
    const file = parameters.get("file");
    const requestedPage = Number.parseInt(parameters.get("page") ?? "1", 10);
    const requestedMergeRequest = Number.parseInt(parameters.get("request") ?? "", 10);
    const requestedReviewTab = parameters.get("tab");
    const requestedMergeRequestState = parameters.get("state");
    const page = Number.isSafeInteger(requestedPage) && requestedPage > 0
        ? requestedPage
        : 1;
    return {
        repository,
        ref: parameters.get("ref") || "",
        path: parameters.get("path") || "",
        file,
        view: requestedView === "history" ||
            requestedView === "builds" ||
            requestedView === "merge-requests" ||
            (requestedView === "blame" && file !== null)
            ? requestedView
            : "files",
        page,
        mergeRequest: Number.isSafeInteger(requestedMergeRequest) && requestedMergeRequest > 0
            ? requestedMergeRequest
            : undefined,
        reviewTab: requestedReviewTab === "changes" ? "changes" : "conversation",
        mergeRequestState: requestedMergeRequestState === "merged" ||
            requestedMergeRequestState === "closed"
            ? requestedMergeRequestState
            : "open",
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
        throw new Error("Clipboard copy failed.");
    }
}
function copyButton(value, description = "clone command") {
    const button = element("button");
    button.type = "button";
    button.className = "icon-button copy-button";
    button.title = `Copy ${description}`;
    button.setAttribute("aria-label", `Copy ${description}`);
    button.append(icon("copy"));
    button.addEventListener("click", async () => {
        try {
            await copyText(value);
            button.classList.add("copied");
            button.replaceChildren(icon("check"));
            button.title = "Copied";
            button.setAttribute("aria-label", `Copied ${description}`);
            window.setTimeout(() => {
                button.classList.remove("copied");
                button.replaceChildren(icon("copy"));
                button.title = `Copy ${description}`;
                button.setAttribute("aria-label", `Copy ${description}`);
            }, 1500);
        }
        catch (reason) {
            showStatus(reason instanceof Error ? reason.message : `Could not copy ${description}.`, true);
        }
    });
    return button;
}
function showGeneratedTokenSecrets(secrets) {
    if (secrets.length === 0) {
        return;
    }
    const dialog = element("dialog");
    dialog.className = "action-dialog clone-dialog";
    const content = element("div");
    content.className = "dialog-form";
    const header = element("div");
    header.className = "dialog-header";
    const title = element("h2", secrets.length === 1 ? "Token secret" : "Token secrets");
    title.id = "token-secret-dialog-title";
    dialog.setAttribute("aria-labelledby", title.id);
    const close = actionButton("Close", "close", "icon-button");
    close.setAttribute("aria-label", "Close");
    close.title = "Close";
    header.append(title, close);
    const warning = element("p", "Copy these generated 32-character secrets now. GitOne will not show them again.");
    warning.className = "dialog-description";
    content.append(header, warning);
    let firstInput;
    for (const generated of secrets) {
        const label = element("label", `${generated.name} (${generated.key})`);
        const field = element("div");
        field.className = "clone-field";
        const input = element("input");
        input.value = generated.secret;
        input.readOnly = true;
        input.spellcheck = false;
        input.autocomplete = "off";
        input.setAttribute("aria-label", `Generated secret for ${generated.name}`);
        input.addEventListener("focus", () => input.select());
        firstInput ??= input;
        field.append(input, copyButton(generated.secret, `secret for ${generated.name}`));
        label.append(field);
        content.append(label);
    }
    const actions = element("div");
    actions.className = "dialog-actions";
    const done = actionButton("Done", undefined, "primary");
    actions.append(done);
    content.append(actions);
    dialog.append(content);
    document.body.append(dialog);
    const dismiss = () => dialog.close();
    close.addEventListener("click", dismiss);
    done.addEventListener("click", dismiss);
    dialog.addEventListener("cancel", (event) => {
        event.preventDefault();
    });
    dialog.addEventListener("close", () => dialog.remove());
    dialog.showModal();
    firstInput?.focus();
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
function subgroupRenameControl(groupPath) {
    const parts = groupPath.split("/");
    const currentName = parts.pop() ?? groupPath;
    const parentPath = parts.join("/");
    const trigger = actionButton("Rename", "pencil", "secondary");
    const dialog = element("dialog");
    dialog.className = "action-dialog";
    const form = element("form");
    form.className = "dialog-form";
    const header = element("div");
    header.className = "dialog-header";
    const title = element("h2", "Rename subgroup");
    const close = actionButton("Close", "close", "icon-button");
    close.setAttribute("aria-label", "Close");
    close.title = "Close";
    header.append(title, close);
    const name = element("input");
    name.name = "name";
    name.required = true;
    name.maxLength = 100;
    name.autocomplete = "off";
    name.spellcheck = false;
    const actions = element("div");
    actions.className = "dialog-actions";
    const cancel = actionButton("Cancel", undefined, "secondary");
    const submit = actionButton("Rename subgroup", "pencil", "primary");
    submit.type = "submit";
    actions.append(cancel, submit);
    form.append(header, fieldLabel("New subgroup name", name), actions);
    dialog.append(form);
    trigger.addEventListener("click", () => {
        name.value = currentName;
        name.setCustomValidity("");
        dialog.showModal();
        name.select();
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
    name.addEventListener("input", () => name.setCustomValidity(""));
    form.addEventListener("submit", async (event) => {
        event.preventDefault();
        const renamedName = name.value.trim();
        if (!renamedName || renamedName.includes("/")) {
            name.setCustomValidity("Subgroup name must be one path segment.");
            name.reportValidity();
            return;
        }
        if (renamedName === currentName) {
            name.setCustomValidity("Enter a different subgroup name.");
            name.reportValidity();
            return;
        }
        const renamedPath = `${parentPath}/${renamedName}`;
        submit.disabled = true;
        cancel.disabled = true;
        close.disabled = true;
        form.setAttribute("aria-busy", "true");
        try {
            await request(apiGroupURL(groupPath), {
                method: "PATCH",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ newPath: renamedPath }),
            });
            dialog.close();
            window.history.replaceState({}, "", groupURL(renamedPath));
            await renderGroup(renamedPath, "Subgroup renamed.");
        }
        catch (reason) {
            showStatus(reason instanceof Error ? reason.message : "Could not rename the subgroup.", true);
            submit.disabled = false;
            cancel.disabled = false;
            close.disabled = false;
            form.removeAttribute("aria-busy");
            name.focus();
        }
    });
    return { trigger, dialog };
}
function emptyState(message) {
    const empty = element("div");
    empty.className = "empty-state";
    empty.append(icon("folder"), element("p", message));
    return empty;
}
function groupRoleBadge(role) {
    const roleName = role[0].toUpperCase() + role.slice(1);
    const badge = element("span", `${roleName} access`);
    badge.className = "group-role-badge";
    badge.title = `Your permission for this group: ${roleName}`;
    return badge;
}
function groupList(groups, emptyMessage = "No groups yet.", showDescriptions = true) {
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
        const title = element("span");
        title.className = "resource-title";
        const name = element("strong", group.name);
        title.append(name, groupRoleBadge(group.role));
        content.append(title);
        if (showDescriptions) {
            const description = element("span", group.description || "No description");
            description.className = "resource-description";
            content.append(description);
        }
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
function repositoryRenameControl(route, groupPath, repositoryName) {
    const trigger = actionButton("Rename", "pencil", "secondary");
    const dialog = element("dialog");
    dialog.className = "action-dialog";
    const form = element("form");
    form.className = "dialog-form";
    const header = element("div");
    header.className = "dialog-header";
    const title = element("h2", "Rename repository");
    const close = actionButton("Close", "close", "icon-button");
    close.setAttribute("aria-label", "Close");
    close.title = "Close";
    header.append(title, close);
    const description = element("p", "The repository URL will change. Git data, LFS objects, builds, and merge requests move with it.");
    description.className = "dialog-description";
    const name = element("input");
    name.name = "name";
    name.required = true;
    name.autocomplete = "off";
    name.spellcheck = false;
    const actions = element("div");
    actions.className = "dialog-actions";
    const cancel = actionButton("Cancel", undefined, "secondary");
    const submit = actionButton("Rename repository", "pencil", "primary");
    submit.type = "submit";
    actions.append(cancel, submit);
    form.append(header, description, fieldLabel("New repository name", name), actions);
    dialog.append(form);
    trigger.addEventListener("click", () => {
        name.value = repositoryName;
        name.setCustomValidity("");
        dialog.showModal();
        name.select();
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
    name.addEventListener("input", () => name.setCustomValidity(""));
    form.addEventListener("submit", async (event) => {
        event.preventDefault();
        const submittedName = name.value.trim();
        const renamedName = submittedName.endsWith(".git")
            ? submittedName.slice(0, -4)
            : submittedName;
        if (renamedName === repositoryName) {
            name.setCustomValidity("Enter a different repository name.");
            name.reportValidity();
            return;
        }
        submit.disabled = true;
        cancel.disabled = true;
        close.disabled = true;
        form.setAttribute("aria-busy", "true");
        try {
            await request(`/api/repositories/${encodeURIComponent(route.repository)}`, {
                method: "PATCH",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ newName: submittedName }),
            });
            window.location.assign(repositoryBrowserURL(`${groupPath}/${renamedName}`, {
                ref: route.ref,
                path: route.path,
                file: route.file ?? undefined,
                view: route.view,
                page: route.page,
                mergeRequest: route.mergeRequest,
                reviewTab: route.reviewTab,
                mergeRequestState: route.mergeRequestState,
            }));
        }
        catch (reason) {
            showStatus(reason instanceof Error ? reason.message : "Could not rename the repository.", true);
            submit.disabled = false;
            cancel.disabled = false;
            close.disabled = false;
            form.removeAttribute("aria-busy");
            name.focus();
        }
    });
    return { trigger, dialog };
}
function repositoryDefaultBranchControl(route, branches) {
    if (!branches.canManage ||
        route.ref === branches.defaultBranch ||
        !branches.branches.some((branch) => branch.name === route.ref)) {
        return null;
    }
    const trigger = actionButton("Set as default", "check", "secondary");
    trigger.title = `Make ${route.ref} the default branch`;
    trigger.addEventListener("click", async () => {
        trigger.disabled = true;
        try {
            await request(repositoryDefaultBranchAPIURL(route.repository), {
                method: "PUT",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ branch: route.ref }),
            });
            window.location.assign(repositoryBrowserURL(route.repository));
        }
        catch (reason) {
            showStatus(reason instanceof Error ? reason.message : "Could not change the default branch.", true);
            trigger.disabled = false;
        }
    });
    return trigger;
}
function repositoryImportControl(groupPath) {
    const trigger = actionButton("Import bare Git", "download", "primary");
    const dialog = element("dialog");
    dialog.className = "action-dialog repository-import-dialog";
    const form = element("form");
    form.className = "dialog-form";
    const header = element("div");
    header.className = "dialog-header";
    const title = element("h2", "Import bare Git repository");
    const close = actionButton("Close", "close", "icon-button");
    close.setAttribute("aria-label", "Close");
    close.title = "Close";
    header.append(title, close);
    const description = element("p", "Import all Git refs and tags from a remote or an uploaded archive. Remote imports also copy and verify every reachable Git LFS object; archive uploads contain Git data only.");
    description.className = "dialog-description";
    const methodTabs = element("div");
    methodTabs.className = "segmented-control repository-import-method";
    methodTabs.setAttribute("role", "tablist");
    methodTabs.setAttribute("aria-label", "Import source");
    const remoteTab = actionButton("HTTP/HTTPS remote");
    remoteTab.className = "segment active";
    remoteTab.setAttribute("role", "tab");
    remoteTab.setAttribute("aria-selected", "true");
    const archiveTab = actionButton("ZIP/TAR upload");
    archiveTab.className = "segment";
    archiveTab.setAttribute("role", "tab");
    archiveTab.setAttribute("aria-selected", "false");
    methodTabs.append(remoteTab, archiveTab);
    const remoteURL = element("input");
    remoteURL.name = "remoteURL";
    remoteURL.type = "url";
    remoteURL.required = true;
    remoteURL.autocomplete = "off";
    remoteURL.pattern = "https?://.+";
    remoteURL.placeholder = "https://git.example.com/team/project.git";
    const remotePanel = element("div");
    remotePanel.className = "repository-import-panel";
    const name = element("input");
    name.name = "name";
    name.required = true;
    name.autocomplete = "off";
    name.placeholder = "project";
    let suggestedName = "";
    const suggestName = (value) => {
        if (name.value !== "" && name.value !== suggestedName) {
            return;
        }
        suggestedName = value;
        name.value = value;
    };
    remoteURL.addEventListener("input", () => {
        try {
            const remote = new URL(remoteURL.value);
            const lastPart = remote.pathname.split("/").filter(Boolean).at(-1) ?? "";
            suggestName(decodeURIComponent(lastPart).replace(/\.git$/i, ""));
        }
        catch {
            suggestName("");
        }
    });
    const username = element("input");
    username.name = "username";
    username.autocomplete = "off";
    username.placeholder = "Optional";
    const password = element("input");
    password.name = "password";
    password.type = "password";
    password.autocomplete = "off";
    password.placeholder = "Optional password or access token";
    const authentication = element("div");
    authentication.className = "repository-import-authentication";
    authentication.append(fieldLabel("Username", username), fieldLabel("Password or token", password));
    remotePanel.append(fieldLabel("Remote HTTP/HTTPS URL", remoteURL), authentication);
    const archiveFile = element("input");
    archiveFile.name = "archive";
    archiveFile.type = "file";
    archiveFile.accept = [
        ".zip",
        ".tar",
        ".tar.gz",
        ".tgz",
        "application/zip",
        "application/x-tar",
        "application/gzip",
    ].join(",");
    const archiveHint = element("p", "Upload up to 1 GiB. The bare repository can be at the archive root or inside one enclosing folder. Links and special files are rejected.");
    archiveHint.className = "dialog-description repository-import-hint";
    const archivePanel = element("div");
    archivePanel.className = "repository-import-panel";
    archivePanel.hidden = true;
    archivePanel.append(fieldLabel("Bare repository archive", archiveFile), archiveHint);
    archiveFile.addEventListener("change", () => {
        const filename = archiveFile.files?.[0]?.name ?? "";
        suggestName(filename
            .replace(/\.(?:tar\.gz|tgz|zip|tar)$/i, "")
            .replace(/\.git$/i, ""));
    });
    let method = "remote";
    const selectMethod = (next, focus = true) => {
        method = next;
        const remote = next === "remote";
        remotePanel.hidden = !remote;
        archivePanel.hidden = remote;
        remoteURL.required = remote;
        archiveFile.required = !remote;
        remoteTab.classList.toggle("active", remote);
        archiveTab.classList.toggle("active", !remote);
        remoteTab.setAttribute("aria-selected", String(remote));
        archiveTab.setAttribute("aria-selected", String(!remote));
        if (focus) {
            if (remote) {
                remoteURL.focus();
            }
            else {
                archiveFile.focus();
            }
        }
    };
    remoteTab.addEventListener("click", () => selectMethod("remote"));
    archiveTab.addEventListener("click", () => selectMethod("archive"));
    const actions = element("div");
    actions.className = "dialog-actions";
    const cancel = actionButton("Cancel", undefined, "secondary");
    const submit = actionButton("Import repository", "download", "primary");
    submit.type = "submit";
    actions.append(cancel, submit);
    form.append(header, description, methodTabs, remotePanel, archivePanel, fieldLabel("Repository name", name), actions);
    dialog.append(form);
    trigger.addEventListener("click", () => {
        dialog.showModal();
        remoteURL.focus();
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
            form.reset();
            suggestedName = "";
            selectMethod("remote", false);
        }
    });
    form.addEventListener("submit", async (event) => {
        event.preventDefault();
        submit.disabled = true;
        cancel.disabled = true;
        close.disabled = true;
        form.setAttribute("aria-busy", "true");
        const repositoryName = name.value.trim();
        try {
            const repositoryPath = encodeURIComponent(`${groupPath}/${repositoryName}`);
            if (method === "remote") {
                await request(`/api/repositories/${repositoryPath}/import`, {
                    method: "POST",
                    headers: { "Content-Type": "application/json" },
                    body: JSON.stringify({
                        url: remoteURL.value.trim(),
                        username: username.value.trim(),
                        password: password.value,
                    }),
                });
            }
            else {
                const archive = archiveFile.files?.[0];
                if (!archive) {
                    throw new Error("Choose a ZIP or TAR archive to upload.");
                }
                if (archive.size > 1024 * 1024 * 1024) {
                    throw new Error("Archive upload exceeds the 1 GiB limit.");
                }
                await request(`/api/repositories/${repositoryPath}/import-archive?filename=${encodeURIComponent(archive.name)}`, {
                    method: "POST",
                    headers: {
                        "Content-Type": archive.type || "application/octet-stream",
                    },
                    body: archive,
                });
            }
            password.value = "";
            dialog.close();
            await renderGroup(groupPath, `Repository ${repositoryName} imported.`);
        }
        catch (reason) {
            showStatus(reason instanceof Error ? reason.message : "Could not import the repository.", true);
        }
        finally {
            submit.disabled = false;
            cancel.disabled = false;
            close.disabled = false;
            form.removeAttribute("aria-busy");
        }
    });
    return { trigger, dialog };
}
function pageHeader(eyebrow, title, description = "", actions = [], badges = []) {
    const header = element("section");
    header.className = "page-header";
    const copy = element("div");
    copy.className = "page-header-copy";
    const context = element("div");
    context.className = "page-header-context";
    const label = element("span", eyebrow);
    label.className = "eyebrow";
    context.append(label, ...badges);
    copy.append(context);
    if (title) {
        copy.append(element("h1", title));
    }
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
function roleSelect(value, canSelectOwner = true) {
    const select = element("select");
    for (const role of ["read", "developer", "maintainer", "owner"]) {
        const option = element("option", role[0].toUpperCase() + role.slice(1));
        option.value = role;
        option.selected = role === value;
        option.disabled = role === "owner" && !canSelectOwner;
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
function groupSettingsControl(path, settings, role) {
    const canManageOwnerSettings = role === "owner";
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
    generalGrid.append(fieldLabel("Group name", name), fieldLabel("Current path", currentPath), fieldLabel("Control version", version), fieldLabel("Description", description));
    generalPanel.append(generalGrid);
    const accessPanel = element("section");
    accessPanel.className = "settings-panel-view";
    accessPanel.setAttribute("role", "tabpanel");
    const accessHeader = element("div");
    accessHeader.className = "settings-section-header";
    accessHeader.append(element("h3", "Members"));
    const addMember = actionButton("Add member", "plus", "secondary");
    addMember.disabled = !canManageOwnerSettings;
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
        memberName.disabled = !canManageOwnerSettings;
        const memberRole = roleSelect(role);
        memberRole.className = "member-role";
        memberRole.disabled = !canManageOwnerSettings;
        const remove = removeButton(`Remove ${username || "member"}`);
        remove.disabled = !canManageOwnerSettings;
        remove.addEventListener("click", () => row.remove());
        row.append(legend, fieldLabel("Canonical LDAP identity", memberName), fieldLabel("Role", memberRole), remove);
        members.append(row);
        if (!username) {
            memberName.focus();
        }
    };
    for (const [username, role] of Object.entries(settings.members).sort()) {
        addMemberRow(username, role);
    }
    addMember.addEventListener("click", () => addMemberRow());
    accessPanel.append(accessHeader);
    if (!canManageOwnerSettings) {
        const notice = element("p", "Only group owners can change members and roles.");
        notice.className = "settings-empty";
        accessPanel.append(notice);
    }
    accessPanel.append(members);
    const tokensPanel = element("section");
    tokensPanel.className = "settings-panel-view";
    tokensPanel.setAttribute("role", "tabpanel");
    const tokensHeader = element("div");
    tokensHeader.className = "settings-section-header";
    tokensHeader.append(element("h3", "Tokens"));
    const addToken = actionButton("Add token", "plus", "secondary");
    tokensHeader.append(addToken);
    if (!canManageOwnerSettings) {
        const notice = element("p", "Only group owners can create or change owner tokens.");
        notice.className = "settings-empty";
        tokensPanel.append(tokensHeader, notice);
    }
    else {
        tokensPanel.append(tokensHeader);
    }
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
        role: "developer",
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
        const tokenRole = roleSelect(token.role, canManageOwnerSettings);
        tokenRole.className = "token-role";
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
        const regenerate = element("input");
        regenerate.className = "token-regenerate";
        regenerate.type = "checkbox";
        const regenerateLabel = element("label");
        regenerateLabel.className = "checkbox-label settings-checkbox";
        const regenerateText = document.createTextNode("");
        regenerateLabel.append(regenerate, regenerateText);
        let regenerateExisting = false;
        regenerate.addEventListener("change", () => {
            regenerateExisting = regenerate.checked;
        });
        const refreshRegenerate = () => {
            const existingKey = token.key !== "" && tokenKey.value.trim() === token.key;
            regenerate.disabled = !existingKey;
            regenerate.checked = existingKey ? regenerateExisting : true;
            regenerateText.textContent = existingKey
                ? "Generate a new 32-character secret on save"
                : "A new 32-character secret will be generated on save";
        };
        tokenKey.addEventListener("input", refreshRegenerate);
        refreshRegenerate();
        fields.append(fieldLabel("Token name", tokenName), fieldLabel("Login key", tokenKey), fieldLabel("Role", tokenRole), fieldLabel("Expires", tokenExpiry), disabledLabel, regenerateLabel);
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
    tokensPanel.append(tokenEmpty, tokens);
    refreshTokenEmpty();
    const policyPanel = element("section");
    policyPanel.className = "settings-panel-view";
    policyPanel.setAttribute("role", "tabpanel");
    policyPanel.append(element("h3", "Repository policy"));
    const policyGrid = element("div");
    policyGrid.className = "settings-field-grid";
    const visibility = element("select");
    for (const [value, label] of [
        ["private", "Private"],
        ["internal", "Internal"],
        ["public", "Public"],
    ]) {
        const option = element("option", label);
        option.value = value;
        option.selected = value === settings.visibility;
        visibility.append(option);
    }
    visibility.disabled = !canManageOwnerSettings;
    const lfsEnabled = element("input");
    lfsEnabled.type = "checkbox";
    lfsEnabled.checked = settings.lfs.enabled;
    lfsEnabled.disabled = !canManageOwnerSettings;
    const lfsLabel = element("label");
    lfsLabel.className = "checkbox-label settings-checkbox";
    lfsLabel.append(lfsEnabled, document.createTextNode("Enable Git LFS for repositories in this group"));
    const maximumObject = element("input");
    maximumObject.type = "number";
    maximumObject.min = "0";
    maximumObject.step = "1";
    maximumObject.value = settings.lfs.maximumObjectBytes
        ? String(settings.lfs.maximumObjectBytes)
        : "";
    maximumObject.disabled = !canManageOwnerSettings;
    const maximumStorage = element("input");
    maximumStorage.type = "number";
    maximumStorage.min = "0";
    maximumStorage.step = "1";
    maximumStorage.value = settings.lfs.maximumStorageBytes
        ? String(settings.lfs.maximumStorageBytes)
        : "";
    maximumStorage.disabled = !canManageOwnerSettings;
    policyGrid.append(fieldLabel("Visibility", visibility), lfsLabel, fieldLabel("Maximum object bytes", maximumObject), fieldLabel("Maximum group storage bytes", maximumStorage));
    if (!canManageOwnerSettings) {
        const notice = element("p", "Only group owners can change repository visibility and LFS policy.");
        notice.className = "settings-empty";
        policyPanel.append(notice);
    }
    policyPanel.append(policyGrid);
    const panelDefinitions = [
        ["General", generalPanel],
        ["Access", accessPanel],
        ["Tokens", tokensPanel],
        ["Policy", policyPanel],
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
                    throw new Error("Every member needs a canonical LDAP identity.");
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
                if (!tokenName || !key) {
                    throw new Error("Every token needs a name and key.");
                }
                const expiry = row.querySelector(".token-expires")?.value ?? "";
                updatedTokens.push({
                    name: tokenName,
                    key,
                    role: row.querySelector(".token-role")?.value,
                    expiresAt: expiry ? new Date(expiry).toISOString() : undefined,
                    disabled: row.querySelector(".token-disabled")?.checked ?? false,
                    regenerate: row.querySelector(".token-regenerate")?.checked ?? false,
                });
            }
            const maximumObjectValue = maximumObject.value.trim();
            const maximumStorageValue = maximumStorage.value.trim();
            const maximumObjectBytes = maximumObjectValue ? Number(maximumObjectValue) : 0;
            const maximumStorageBytes = maximumStorageValue ? Number(maximumStorageValue) : 0;
            if (!Number.isSafeInteger(maximumObjectBytes) ||
                !Number.isSafeInteger(maximumStorageBytes) ||
                maximumObjectBytes < 0 ||
                maximumStorageBytes < 0) {
                throw new Error("LFS limits must be non-negative whole bytes.");
            }
            const updated = await request(groupSettingsAPIURL(path), {
                method: "PUT",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({
                    name: name.value.trim(),
                    description: description.value,
                    inherit: settings.inherit,
                    visibility: visibility.value,
                    lfs: {
                        enabled: lfsEnabled.checked,
                        maximumObjectBytes,
                        maximumStorageBytes,
                    },
                    members: updatedMembers,
                    tokens: updatedTokens,
                }),
            });
            dialog.close();
            window.history.replaceState({}, "", groupURL(updated.path));
            showGeneratedTokenSecrets(updated.generatedSecrets ?? []);
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
        if (build.status === "canceled") {
            return "Canceled before start";
        }
        if (build.status === "manual") {
            return "Waiting for manual start";
        }
        return build.status === "waiting" ? "Waiting for dependencies" : "Waiting to start";
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
function repositoryPipelines(builds) {
    const pipelines = new Map();
    for (const build of builds) {
        const key = JSON.stringify([build.branch, build.commit, build.createdAt]);
        const pipeline = pipelines.get(key);
        if (pipeline) {
            pipeline.jobs.push(build);
            continue;
        }
        pipelines.set(key, {
            key,
            branch: build.branch,
            commit: build.commit,
            createdAt: build.createdAt,
            jobs: [build],
        });
    }
    return [...pipelines.values()];
}
function aggregateBuildStatus(jobs) {
    for (const status of [
        "failed",
        "running",
        "queued",
        "waiting",
        "canceled",
        "manual",
        "succeeded",
    ]) {
        if (jobs.some((job) => job.status === status)) {
            return status;
        }
    }
    return "succeeded";
}
function pipelineStages(jobs) {
    const byID = new Map(jobs.map((job) => [job.id, job]));
    const depths = new Map();
    const visiting = new Set();
    const depth = (job) => {
        const known = depths.get(job.id);
        if (known !== undefined) {
            return known;
        }
        if (visiting.has(job.id)) {
            return 0;
        }
        visiting.add(job.id);
        const dependencies = (job.needs ?? [])
            .map((need) => byID.get(need.id))
            .filter((dependency) => dependency !== undefined);
        const value = dependencies.length === 0
            ? 0
            : Math.max(...dependencies.map((dependency) => depth(dependency))) + 1;
        visiting.delete(job.id);
        depths.set(job.id, value);
        return value;
    };
    const stages = [];
    for (const job of jobs) {
        const index = depth(job);
        (stages[index] ??= []).push(job);
    }
    return stages;
}
function buildStatusBadge(status, subject = "Build") {
    const badge = element("span");
    badge.className = `build-status build-status-${status}`;
    const statusIcon = status === "succeeded"
        ? "check"
        : status === "failed" || status === "canceled"
            ? "close"
            : status === "manual" ? "play" : "clock";
    const label = status[0].toUpperCase() + status.slice(1);
    badge.append(icon(statusIcon), document.createTextNode(label));
    badge.setAttribute("aria-label", `${subject} status: ${label}`);
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
    const latestJobs = () => {
        const latest = data.builds.find((build) => build.branch === route.ref);
        if (!latest) {
            return [];
        }
        return data.builds.filter((build) => build.branch === latest.branch &&
            build.commit === latest.commit &&
            build.createdAt === latest.createdAt);
    };
    const render = () => {
        const label = element("span", "Latest pipeline");
        label.className = "latest-build-label";
        const jobs = latestJobs();
        if (jobs.length === 0) {
            const badge = element("span");
            badge.className = "build-status build-status-none";
            badge.append(icon("clock"), document.createTextNode("None"));
            link.replaceChildren(label, badge);
            link.title = `No builds have run on ${route.ref}`;
            link.setAttribute("aria-label", `Latest build on ${route.ref}: none`);
            return;
        }
        const build = jobs[0];
        const status = aggregateBuildStatus(jobs);
        const badge = buildStatusBadge(status, "Pipeline");
        link.replaceChildren(label, badge);
        link.title = [
            `Latest pipeline on ${route.ref}: ${status}`,
            `${jobs.length} ${jobs.length === 1 ? "job" : "jobs"}`,
            shortCommitHash(build.commit),
            relativeTime(build.createdAt),
        ].join(" · ");
        link.setAttribute("aria-label", `Latest pipeline on ${route.ref}: ${status} at ${shortCommitHash(build.commit)}`);
    };
    const scheduleRefresh = () => {
        if (canceled) {
            return;
        }
        const jobs = latestJobs();
        const active = jobs.some((job) => job.status === "waiting" || job.status === "queued" || job.status === "running");
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
    const heading = sectionHeading("Pipelines", repositoryPipelines(initial.builds).length, [
        refreshState,
        refreshButton,
    ]);
    const listRoot = element("div");
    listRoot.className = "build-list-root";
    const logDialog = element("dialog");
    logDialog.className = "action-dialog build-log-dialog";
    logDialog.id = "build-log-dialog";
    const logDialogContent = element("div");
    logDialogContent.className = "dialog-form build-log-dialog-content";
    const logHeader = element("div");
    logHeader.className = "dialog-header";
    const logTitle = element("h2", "Build log");
    logTitle.id = "build-log-dialog-title";
    logDialog.setAttribute("aria-labelledby", logTitle.id);
    const closeLog = actionButton("Close", "close", "icon-button");
    closeLog.setAttribute("aria-label", "Close build log");
    closeLog.title = "Close";
    logHeader.append(logTitle, closeLog);
    const logToolbar = element("div");
    logToolbar.className = "build-log-dialog-toolbar";
    const logContext = element("div");
    logContext.className = "build-log-dialog-context";
    const refreshLog = actionButton("Refresh log", "refresh", "secondary");
    logToolbar.append(logContext, refreshLog);
    const logOutput = element("pre", "No log output yet.");
    logOutput.className = "build-log build-log-dialog-output";
    logOutput.tabIndex = 0;
    logDialogContent.append(logHeader, logToolbar, logOutput);
    logDialog.append(logDialogContent);
    section.append(heading, listRoot, logDialog);
    let data = initial;
    let canceled = false;
    let refreshing = false;
    let timer;
    let activeLogBuildID = null;
    let logTrigger = null;
    const logs = new Map();
    const loadingLogs = new Set();
    const mutatingBuilds = new Set();
    let dependencyFrame;
    const drawDependencyLines = () => {
        dependencyFrame = undefined;
        for (const canvas of listRoot.querySelectorAll(".pipeline-graph-canvas")) {
            const svg = canvas.querySelector(".pipeline-dependencies");
            if (!svg) {
                continue;
            }
            const canvasBounds = canvas.getBoundingClientRect();
            const width = Math.ceil(canvasBounds.width);
            const height = Math.ceil(canvasBounds.height);
            svg.setAttribute("width", String(width));
            svg.setAttribute("height", String(height));
            svg.setAttribute("viewBox", `0 0 ${width} ${height}`);
            svg.replaceChildren();
            const cards = new Map();
            for (const card of canvas.querySelectorAll(".build-item")) {
                if (card.dataset.buildId) {
                    cards.set(card.dataset.buildId, card);
                }
            }
            for (const target of cards.values()) {
                for (const dependencyID of (target.dataset.needs ?? "").split(" ").filter(Boolean)) {
                    const source = cards.get(dependencyID);
                    if (!source) {
                        continue;
                    }
                    const sourceBounds = source.getBoundingClientRect();
                    const targetBounds = target.getBoundingClientRect();
                    const startX = sourceBounds.right - canvasBounds.left;
                    const startY = sourceBounds.top + sourceBounds.height / 2 - canvasBounds.top;
                    const endX = targetBounds.left - canvasBounds.left;
                    const endY = targetBounds.top + targetBounds.height / 2 - canvasBounds.top;
                    if (endX <= startX) {
                        continue;
                    }
                    const bend = Math.max(24, (endX - startX) * 0.45);
                    const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
                    path.classList.add("pipeline-dependency-line");
                    path.setAttribute("d", `M ${startX} ${startY} C ${startX + bend} ${startY}, ${endX - bend} ${endY}, ${endX} ${endY}`);
                    svg.append(path);
                }
            }
        }
    };
    const scheduleDependencyLines = () => {
        if (dependencyFrame !== undefined) {
            window.cancelAnimationFrame(dependencyFrame);
        }
        dependencyFrame = window.requestAnimationFrame(drawDependencyLines);
    };
    const renderLogDialog = () => {
        if (activeLogBuildID === null) {
            return;
        }
        const build = data.builds.find((candidate) => candidate.id === activeLogBuildID);
        if (!build) {
            logTitle.textContent = "Build log";
            logContext.replaceChildren(element("span", "Build is no longer available."));
            logOutput.textContent = "No log output available.";
            refreshLog.disabled = true;
            return;
        }
        const pinned = logOutput.scrollHeight - logOutput.scrollTop - logOutput.clientHeight < 24;
        const scrollTop = logOutput.scrollTop;
        const contents = loadingLogs.has(build.id) && !logs.has(build.id)
            ? "Loading build log…"
            : logs.get(build.id) ?? "No log output yet.";
        logTitle.textContent = `Build log · ${build.name}`;
        const branch = element("span", build.branch);
        branch.prepend(icon("git-branch"));
        logContext.replaceChildren(buildStatusBadge(build.status), branch, element("code", shortCommitHash(build.commit)));
        refreshLog.disabled = loadingLogs.has(build.id);
        logOutput.dataset.buildId = build.id;
        if (logOutput.textContent !== contents) {
            logOutput.textContent = contents;
            window.requestAnimationFrame(() => {
                logOutput.scrollTop = pinned ? logOutput.scrollHeight : scrollTop;
            });
        }
    };
    const openBuildLog = (build, trigger) => {
        activeLogBuildID = build.id;
        logTrigger = trigger;
        logOutput.scrollTop = 0;
        renderLogDialog();
        if (!logDialog.open) {
            logDialog.showModal();
        }
        logOutput.focus();
        void loadLog(build.id);
    };
    closeLog.addEventListener("click", () => logDialog.close());
    refreshLog.addEventListener("click", () => {
        if (activeLogBuildID !== null) {
            void loadLog(activeLogBuildID);
        }
    });
    logDialog.addEventListener("click", (event) => {
        if (event.target === logDialog) {
            logDialog.close();
        }
    });
    logDialog.addEventListener("close", () => {
        const closedBuildID = activeLogBuildID;
        const currentTrigger = closedBuildID === null
            ? null
            : listRoot.querySelector(`.build-log-toggle[data-build-id="${CSS.escape(closedBuildID)}"]`);
        activeLogBuildID = null;
        (logTrigger?.isConnected ? logTrigger : currentTrigger)?.focus();
        logTrigger = null;
    });
    const renderBuilds = () => {
        const graphScrollPositions = new Map();
        for (const graph of listRoot.querySelectorAll(".pipeline-graph-scroll")) {
            if (graph.dataset.pipelineKey) {
                graphScrollPositions.set(graph.dataset.pipelineKey, graph.scrollLeft);
            }
        }
        const pipelines = repositoryPipelines(data.builds);
        const count = heading.querySelector(".count-badge");
        if (count) {
            count.textContent = String(pipelines.length);
        }
        if (data.builds.length === 0) {
            listRoot.replaceChildren(emptyState("No pipelines yet. Push a branch containing a .gitone.yaml build definition."));
            return;
        }
        const list = element("ol");
        list.className = "pipeline-list";
        for (const pipeline of pipelines) {
            const pipelineItem = element("li");
            const pipelineStatus = aggregateBuildStatus(pipeline.jobs);
            pipelineItem.className = `pipeline-item pipeline-item-${pipelineStatus}`;
            const pipelineHeader = element("div");
            pipelineHeader.className = "pipeline-header";
            const pipelineIdentity = element("div");
            pipelineIdentity.className = "pipeline-identity";
            const pipelineTitle = element("div");
            pipelineTitle.className = "pipeline-title";
            pipelineTitle.append(element("strong", `Pipeline · ${pipeline.branch}`), element("code", shortCommitHash(pipeline.commit)));
            const pipelineMetadata = element("div");
            pipelineMetadata.className = "pipeline-metadata";
            const created = element("span", `Created ${relativeTime(pipeline.createdAt)}`);
            created.title = new Date(pipeline.createdAt).toLocaleString();
            pipelineMetadata.append(created, element("span", `${pipeline.jobs.length} ${pipeline.jobs.length === 1 ? "job" : "jobs"}`));
            pipelineIdentity.append(pipelineTitle, pipelineMetadata);
            pipelineHeader.append(pipelineIdentity, buildStatusBadge(pipelineStatus, "Pipeline"));
            const graphScroll = element("div");
            graphScroll.className = "pipeline-graph-scroll";
            graphScroll.dataset.pipelineKey = pipeline.key;
            graphScroll.setAttribute("aria-label", `Job dependencies for pipeline on ${pipeline.branch} at ${shortCommitHash(pipeline.commit)}`);
            const graph = element("div");
            graph.className = "pipeline-graph-canvas";
            const dependencies = document.createElementNS("http://www.w3.org/2000/svg", "svg");
            dependencies.classList.add("pipeline-dependencies");
            dependencies.setAttribute("aria-hidden", "true");
            graph.append(dependencies);
            const stages = pipelineStages(pipeline.jobs);
            stages.forEach((jobs, stageIndex) => {
                const stage = element("section");
                stage.className = "pipeline-stage";
                const stageHeading = element("div");
                stageHeading.className = "pipeline-stage-heading";
                stageHeading.append(element("strong", stageIndex === 0 ? "Starts pipeline" : `Stage ${stageIndex + 1}`), element("span", `${jobs.length} ${jobs.length === 1 ? "job" : "jobs"}`));
                const stageJobs = element("div");
                stageJobs.className = "pipeline-stage-jobs";
                for (const build of jobs) {
                    const item = element("article");
                    item.className = `build-item build-item-${build.status}`;
                    item.dataset.buildId = build.id;
                    item.dataset.needs = (build.needs ?? []).map((need) => need.id).join(" ");
                    const summary = element("div");
                    summary.className = "build-summary";
                    const identity = element("div");
                    identity.className = "build-identity";
                    const title = element("div");
                    title.className = "build-title";
                    title.append(element("strong", build.name));
                    const metadata = element("div");
                    metadata.className = "build-metadata";
                    metadata.append(element("span", buildDuration(build)), element("span", build.image ? `Image ${build.image}` : "No image"));
                    if (build.needs && build.needs.length > 0) {
                        metadata.append(element("span", `Needs ${build.needs.map((need) => need.name).join(", ")}`));
                    }
                    if (build.rerunOf) {
                        const rerun = element("span", `Re-run of ${build.rerunOf}`);
                        rerun.title = build.rerunOf;
                        metadata.append(rerun);
                    }
                    identity.append(title, metadata);
                    const controls = element("div");
                    controls.className = "build-controls";
                    const logButton = actionButton("View log", "file", "secondary icon-button build-log-toggle");
                    logButton.setAttribute("aria-label", "View log");
                    logButton.title = "View log";
                    logButton.dataset.buildId = build.id;
                    logButton.setAttribute("aria-haspopup", "dialog");
                    logButton.setAttribute("aria-controls", logDialog.id);
                    logButton.addEventListener("click", () => openBuildLog(build, logButton));
                    controls.append(buildStatusBadge(build.status));
                    if (data.canManage) {
                        const active = build.status === "waiting" || build.status === "queued" ||
                            build.status === "running";
                        const manual = build.status === "manual";
                        const mutationButton = actionButton(manual ? "Run" : active ? "Cancel" : "Run again", manual ? "play" : active ? "close" : "refresh", active
                            ? "danger-secondary icon-button build-cancel"
                            : manual ? "primary icon-button build-start" : "secondary icon-button build-rerun");
                        const mutationLabel = manual ? "Run manual job" : active ? "Cancel job" : "Run again";
                        mutationButton.setAttribute("aria-label", mutationLabel);
                        mutationButton.title = mutationLabel;
                        const dependenciesReady = (build.needs ?? []).every((need) => data.builds.find((candidate) => candidate.id === need.id)?.status === "succeeded");
                        mutationButton.disabled = mutatingBuilds.has(build.id) ||
                            (manual && !dependenciesReady);
                        if (manual && !dependenciesReady) {
                            mutationButton.title = "This job can start after all dependencies succeed.";
                        }
                        mutationButton.addEventListener("click", () => {
                            void mutateBuild(build, manual ? "start" : active ? "cancel" : "rerun");
                        });
                        controls.append(mutationButton);
                    }
                    controls.append(logButton);
                    summary.append(identity, controls);
                    item.append(summary);
                    if (build.error) {
                        const error = element("p", build.error);
                        error.className = "build-error";
                        error.setAttribute("role", "alert");
                        item.append(error);
                    }
                    stageJobs.append(item);
                }
                stage.append(stageHeading, stageJobs);
                graph.append(stage);
            });
            graphScroll.append(graph);
            pipelineItem.append(pipelineHeader, graphScroll);
            list.append(pipelineItem);
        }
        listRoot.replaceChildren(list);
        for (const graph of listRoot.querySelectorAll(".pipeline-graph-scroll")) {
            if (graph.dataset.pipelineKey) {
                graph.scrollLeft = graphScrollPositions.get(graph.dataset.pipelineKey) ?? 0;
            }
        }
        scheduleDependencyLines();
    };
    const updateBuild = (updated) => {
        const index = data.builds.findIndex((build) => build.id === updated.id);
        if (index >= 0) {
            data.builds[index] = updated;
        }
    };
    async function mutateBuild(build, action) {
        if (mutatingBuilds.has(build.id) || canceled) {
            return;
        }
        mutatingBuilds.add(build.id);
        renderBuilds();
        try {
            const result = await request(repositoryBuildActionAPIURL(route.repository, build.id, action), { method: "POST" });
            if (canceled) {
                return;
            }
            if (action === "rerun") {
                data.builds.unshift(result.build);
                showStatus(`Job ${result.build.name} queued again.`);
            }
            else {
                updateBuild(result.build);
                showStatus(action === "start"
                    ? `Job ${result.build.name} queued.`
                    : `Job ${result.build.name} canceled.`);
            }
        }
        catch (reason) {
            if (!canceled) {
                showStatus(reason instanceof Error
                    ? `Could not ${action} build: ${reason.message}`
                    : `Could not ${action} build.`, true);
            }
        }
        finally {
            mutatingBuilds.delete(build.id);
            if (!canceled) {
                renderBuilds();
            }
        }
    }
    async function loadLog(id) {
        if (loadingLogs.has(id) || canceled) {
            return;
        }
        loadingLogs.add(id);
        renderLogDialog();
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
                renderLogDialog();
            }
        }
    }
    const scheduleRefresh = () => {
        if (canceled) {
            return;
        }
        const active = data.builds.some((build) => build.status === "waiting" || build.status === "queued" ||
            build.status === "running");
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
            const previouslyActiveLog = activeLogBuildID !== null && data.builds.some((build) => build.id === activeLogBuildID &&
                (build.status === "waiting" || build.status === "queued" ||
                    build.status === "running"));
            const refreshed = await request(repositoryBuildsAPIURL(route.repository));
            const liveLogs = refreshed.builds.filter((build) => build.id === activeLogBuildID &&
                (build.status === "waiting" ||
                    build.status === "queued" ||
                    build.status === "running" ||
                    previouslyActiveLog ||
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
                renderLogDialog();
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
    const resizeHandler = () => scheduleDependencyLines();
    document.addEventListener("visibilitychange", visibilityHandler);
    window.addEventListener("resize", resizeHandler);
    repositoryBuildPollingStop = () => {
        canceled = true;
        activeLogBuildID = null;
        logTrigger = null;
        if (logDialog.open) {
            logDialog.close();
        }
        if (timer !== undefined) {
            window.clearTimeout(timer);
        }
        if (dependencyFrame !== undefined) {
            window.cancelAnimationFrame(dependencyFrame);
        }
        document.removeEventListener("visibilitychange", visibilityHandler);
        window.removeEventListener("resize", resizeHandler);
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
    const pagination = element("nav");
    pagination.className = "history-pagination";
    pagination.setAttribute("aria-label", "Commit history pages");
    const previous = element(data.hasPrevious ? "a" : "button", "Previous");
    previous.className = "button secondary";
    if (previous instanceof HTMLAnchorElement) {
        previous.href = repositoryBrowserURL(route.repository, {
            ref: route.ref,
            view: "history",
            page: data.page - 1,
        });
    }
    else {
        previous.disabled = true;
    }
    const start = (data.page - 1) * data.perPage + 1;
    const end = start + data.commits.length - 1;
    const exactStatus = data.total !== undefined && data.totalPages !== undefined
        ? `Page ${data.page} of ${Math.max(data.totalPages, 1)} · ${data.commits.length === 0 ? "No commits" : `${start}–${end} of ${data.total}`}`
        : `Page ${data.page} · ${start}–${end}`;
    const pageStatus = element("span", exactStatus);
    pageStatus.className = "history-page-status";
    const next = element(data.hasNext ? "a" : "button", "Next");
    next.className = "button secondary";
    if (next instanceof HTMLAnchorElement) {
        next.href = repositoryBrowserURL(route.repository, {
            ref: route.ref,
            view: "history",
            page: data.page + 1,
        });
    }
    else {
        next.disabled = true;
    }
    pagination.append(previous, pageStatus, next);
    section.append(pagination);
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
    if (data.truncated) {
        content.append(comparisonTruncatedNotice());
    }
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
function repositoryNavigation(route, action) {
    const nav = element("nav");
    nav.className = "repository-navigation";
    nav.setAttribute("aria-label", "Repository");
    const tabs = element("div");
    tabs.className = "repository-tabs";
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
    const mergeRequests = element("a");
    mergeRequests.append(icon("git-merge"), document.createTextNode("Merge requests"));
    mergeRequests.href = repositoryBrowserURL(route.repository, {
        view: "merge-requests",
        mergeRequestState: "open",
    });
    if (route.view === "history") {
        history.setAttribute("aria-current", "page");
    }
    else if (route.view === "builds") {
        builds.setAttribute("aria-current", "page");
    }
    else if (route.view === "merge-requests") {
        mergeRequests.setAttribute("aria-current", "page");
    }
    else {
        files.setAttribute("aria-current", "page");
    }
    tabs.append(files, history, builds, mergeRequests);
    nav.append(tabs);
    if (action) {
        action.classList.add("repository-navigation-action");
        nav.append(action);
    }
    return nav;
}
function repositoryBranchCreator(route, data) {
    const trigger = actionButton("New branch", "git-branch", "secondary");
    trigger.classList.add("new-branch-trigger");
    trigger.setAttribute("aria-label", "New branch");
    trigger.title = "New branch";
    const dialog = element("dialog");
    dialog.className = "action-dialog";
    if (!data.canWrite) {
        trigger.disabled = true;
        trigger.title = "Developer access is required to create a branch";
        return { trigger, dialog };
    }
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
function repositoryBranchDivergence(branch) {
    if (!branch.compared) {
        return "Comparison unavailable";
    }
    return `${branch.ahead} ahead · ${branch.behind} behind`;
}
function repositoryBranchManager(route, data) {
    const trigger = actionButton("View all", "git-branch", "secondary");
    trigger.classList.add("branch-manage-trigger");
    trigger.setAttribute("aria-label", "View all branches");
    trigger.title = "View all branches";
    const dialog = element("dialog");
    dialog.className = "action-dialog branch-manager-dialog";
    const shell = element("div");
    shell.className = "branch-manager-shell";
    const header = element("div");
    header.className = "dialog-header";
    const title = element("h2", "Branches");
    const close = actionButton("Close", "close", "icon-button");
    close.setAttribute("aria-label", "Close");
    close.title = "Close";
    header.append(title, close);
    const body = element("div");
    body.className = "branch-manager-body";
    const comparisonReference = data.defaultBranch || data.defaultRef;
    const description = element("p", comparisonReference
        ? `Ahead and behind counts are relative to ${comparisonReference}.`
        : "Ahead and behind counts are unavailable because this repository has no default reference.");
    description.className = "dialog-description branch-manager-description";
    body.append(description);
    let resetManager = () => { };
    let branchesChanged = false;
    let deletedCurrentBranch = false;
    if (data.branches.length === 0) {
        body.append(emptyState("This repository has no branches."));
    }
    else {
        const tableWrapper = element("div");
        tableWrapper.className = "branch-manager-table-wrapper";
        const table = element("table");
        table.className = "branch-manager-table";
        const head = element("thead");
        const heading = element("tr");
        for (const label of ["Branch", "Compared with default", "Commit", "Actions"]) {
            heading.append(element("th", label));
        }
        head.append(heading);
        const rows = element("tbody");
        let activeConfirmation = null;
        let activeDelete = null;
        const clearConfirmation = () => {
            activeConfirmation?.remove();
            activeConfirmation = null;
            if (activeDelete?.isConnected) {
                activeDelete.disabled = false;
            }
            activeDelete = null;
        };
        resetManager = clearConfirmation;
        for (const branch of data.branches) {
            const row = element("tr");
            const nameCell = element("td");
            const name = element("a");
            name.href = repositoryBrowserURL(route.repository, { ref: branch.name });
            name.append(element("code", branch.name));
            const labels = element("span");
            labels.className = "branch-manager-labels";
            if (branch.name === data.defaultBranch) {
                const badge = element("span", "default");
                badge.className = "branch-manager-badge";
                labels.append(badge);
            }
            if (branch.name === route.ref) {
                const badge = element("span", "current");
                badge.className = "branch-manager-badge branch-manager-current";
                labels.append(badge);
            }
            nameCell.append(name, labels);
            const divergence = element("td", repositoryBranchDivergence(branch));
            divergence.className = branch.compared
                ? "branch-manager-divergence"
                : "branch-manager-divergence branch-manager-unavailable";
            const commit = element("td");
            const commitCode = element("code", shortCommitHash(branch.commit));
            commitCode.title = branch.commit;
            commit.append(commitCode);
            const actions = element("td");
            actions.className = "branch-manager-actions";
            const remove = actionButton("Delete", "trash", "danger-secondary");
            if (branch.name === data.defaultBranch) {
                remove.disabled = true;
                remove.title = "Choose another default branch before deleting this branch";
            }
            else if (!data.canWrite) {
                remove.disabled = true;
                remove.title = "Developer access is required to delete a branch";
            }
            else {
                remove.title = `Delete ${branch.name}`;
                remove.addEventListener("click", () => {
                    clearConfirmation();
                    remove.disabled = true;
                    activeDelete = remove;
                    const confirmation = element("tr");
                    confirmation.className = "branch-delete-confirmation";
                    const cell = element("td");
                    cell.colSpan = 4;
                    const form = element("form");
                    const warning = element("p", `Delete ${branch.name}? The branch pointer will be removed and this cannot be undone from the UI.`);
                    const controls = element("div");
                    controls.className = "branch-delete-confirmation-actions";
                    const cancel = actionButton("Cancel", undefined, "secondary");
                    const confirm = actionButton("Delete branch", "trash", "danger");
                    confirm.type = "submit";
                    controls.append(cancel, confirm);
                    form.append(warning, controls);
                    cell.append(form);
                    confirmation.append(cell);
                    row.after(confirmation);
                    activeConfirmation = confirmation;
                    cancel.addEventListener("click", () => {
                        clearConfirmation();
                        remove.focus();
                    });
                    form.addEventListener("submit", async (event) => {
                        event.preventDefault();
                        confirm.disabled = true;
                        cancel.disabled = true;
                        form.setAttribute("aria-busy", "true");
                        try {
                            const deleted = await request(repositoryBranchAPIURL(route.repository, branch.name), {
                                method: "DELETE",
                                headers: { "Content-Type": "application/json" },
                                body: JSON.stringify({ expectedCommit: branch.commit }),
                            });
                            const branchIndex = data.branches.findIndex((candidate) => candidate.name === branch.name);
                            if (branchIndex >= 0) {
                                data.branches.splice(branchIndex, 1);
                            }
                            branchesChanged = true;
                            activeConfirmation = null;
                            activeDelete = null;
                            confirmation.remove();
                            row.remove();
                            if (rows.childElementCount === 0) {
                                tableWrapper.replaceWith(emptyState("This repository has no branches."));
                            }
                            if (route.ref === branch.name) {
                                deletedCurrentBranch = true;
                                const notice = element("p", "The branch currently being viewed was deleted. Closing this window returns to the repository default.");
                                notice.className = "branch-manager-notice";
                                description.after(notice);
                            }
                            showStatus(`${deleted.name} deleted at ${shortCommitHash(deleted.commit)}.`);
                        }
                        catch (reason) {
                            showStatus(reason instanceof Error ? reason.message : "Could not delete the branch.", true);
                            confirm.disabled = false;
                            cancel.disabled = false;
                            form.removeAttribute("aria-busy");
                        }
                    });
                });
            }
            actions.append(remove);
            row.append(nameCell, divergence, commit, actions);
            rows.append(row);
        }
        table.append(head, rows);
        tableWrapper.append(table);
        body.append(tableWrapper);
    }
    shell.append(header, body);
    dialog.append(shell);
    trigger.addEventListener("click", () => dialog.showModal());
    close.addEventListener("click", () => dialog.close());
    dialog.addEventListener("click", (event) => {
        if (event.target === dialog) {
            dialog.close();
        }
    });
    dialog.addEventListener("close", () => {
        resetManager();
        if (deletedCurrentBranch) {
            window.location.assign(repositoryBrowserURL(route.repository));
            return;
        }
        if (branchesChanged) {
            void renderRepositoryBrowser(route);
            return;
        }
        if (trigger.isConnected) {
            trigger.focus();
        }
    });
    return { trigger, dialog };
}
function comparisonStat(label, value) {
    const stat = element("span");
    stat.append(element("strong", value), document.createTextNode(label));
    return stat;
}
function comparisonTruncatedNotice() {
    const notice = element("p", "Diff truncated after 1,000 files, 8 MiB of patches, or an oversized text file.");
    notice.className = "diff-truncated";
    return notice;
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
        if (file.truncated) {
            section.append(comparisonFileTruncatedNotice());
        }
        return section;
    }
    if (file.patch) {
        section.append(diffPatch(file.patch.split("\n")));
    }
    if (file.truncated) {
        section.append(comparisonFileTruncatedNotice());
    }
    return section;
}
function comparisonFileTruncatedNotice() {
    const notice = element("p", "This file's diff was truncated.");
    notice.className = "diff-truncated";
    return notice;
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
    direction.append(element("code", comparison.head), icon("chevron-right"), element("code", comparison.base));
    const stats = element("div");
    stats.className = "comparison-stats";
    const additions = comparison.files.reduce((total, file) => total + file.additions, 0);
    const deletions = comparison.files.reduce((total, file) => total + file.deletions, 0);
    stats.append(comparisonStat("commits ahead", String(comparison.ahead)), comparisonStat("behind", String(comparison.behind)), comparisonStat("files changed", String(comparison.files.length)), comparisonStat("additions", `+${additions}`), comparisonStat("deletions", `−${deletions}`));
    summary.append(direction, stats);
    result.append(summary);
    if (comparison.truncated) {
        result.append(comparisonTruncatedNotice());
    }
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
    if (comparison.canMerge && comparison.ahead > 0) {
        const createAction = actionButton("Create merge request", "git-merge", "primary");
        const creation = element("form");
        creation.className = "merge-request-create";
        creation.hidden = true;
        const requestTitle = element("input");
        requestTitle.name = "title";
        requestTitle.required = true;
        requestTitle.value = `Merge ${comparison.head} into ${comparison.base}`;
        const description = element("textarea");
        description.name = "description";
        description.rows = 5;
        description.placeholder = "Describe the change and anything reviewers should know.";
        const actions = element("div");
        actions.className = "merge-request-create-actions";
        const submit = actionButton("Create merge request", "git-merge", "primary");
        submit.type = "submit";
        const cancel = actionButton("Cancel", undefined, "secondary");
        actions.append(cancel, submit);
        creation.append(fieldLabel("Title", requestTitle), fieldLabel("Description", description), actions);
        createAction.addEventListener("click", () => {
            createAction.hidden = true;
            creation.hidden = false;
            requestTitle.focus();
            requestTitle.select();
        });
        cancel.addEventListener("click", () => {
            creation.hidden = true;
            createAction.hidden = false;
            createAction.focus();
        });
        creation.addEventListener("submit", async (event) => {
            event.preventDefault();
            submit.disabled = true;
            cancel.disabled = true;
            creation.setAttribute("aria-busy", "true");
            try {
                const created = await request(repositoryMergeRequestsAPIURL(route.repository), {
                    method: "POST",
                    headers: { "Content-Type": "application/json" },
                    body: JSON.stringify({
                        title: requestTitle.value.trim(),
                        description: description.value,
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
                    view: "merge-requests",
                    page: 1,
                    mergeRequest: created.id,
                    reviewTab: "conversation",
                    mergeRequestState: created.state,
                };
                window.history.pushState(null, "", repositoryBrowserURL(route.repository, {
                    view: "merge-requests",
                    mergeRequest: created.id,
                    reviewTab: "conversation",
                    mergeRequestState: created.state,
                }));
                await renderRepositoryBrowser(nextRoute);
                showStatus(`Merge request !${created.id} created.`);
            }
            catch (reason) {
                showStatus(reason instanceof Error ? reason.message : "Could not create the merge request.", true);
                submit.disabled = false;
                cancel.disabled = false;
                creation.removeAttribute("aria-busy");
            }
        });
        mergeStatus.append(createAction, creation);
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
    const selectedTarget = data.branches.find((branch) => branch.name === route.ref)?.name
        ?? data.branches.find((branch) => branch.name === data.defaultBranch)?.name
        ?? data.branches[0]?.name
        ?? "";
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
function mergeRequestStateBadge(state) {
    const badge = element("span");
    badge.className = `merge-request-state merge-request-state-${state}`;
    const label = state[0].toUpperCase() + state.slice(1);
    badge.append(icon(state === "merged" ? "git-merge" : state === "closed" ? "close" : "clock"), document.createTextNode(label));
    badge.setAttribute("aria-label", `Merge request state: ${label}`);
    return badge;
}
function mergeRequestDirection(target, source) {
    const direction = element("span");
    direction.className = "merge-request-direction";
    direction.append(element("code", source), icon("chevron-right"), element("code", target));
    direction.setAttribute("aria-label", `${source} into ${target}`);
    return direction;
}
function mergeRequestBrowserURL(route, options = {}) {
    return repositoryBrowserURL(route.repository, {
        view: "merge-requests",
        mergeRequest: options.mergeRequest,
        reviewTab: options.tab,
        mergeRequestState: options.state ?? route.mergeRequestState,
    });
}
async function showUpdatedMergeRequest(route, updated, message) {
    const nextRoute = {
        ...route,
        mergeRequest: updated.id,
        mergeRequestState: updated.state,
    };
    window.history.replaceState(null, "", mergeRequestBrowserURL(nextRoute, {
        mergeRequest: updated.id,
        tab: nextRoute.reviewTab,
        state: updated.state,
    }));
    await renderRepositoryBrowser(nextRoute);
    showStatus(message);
}
function mergeRequestListItem(route, mergeRequest) {
    const item = element("li");
    const link = element("a");
    link.className = "merge-request-list-item";
    link.href = mergeRequestBrowserURL(route, {
        mergeRequest: mergeRequest.id,
        tab: "conversation",
        state: route.mergeRequestState,
    });
    const header = element("div");
    header.className = "merge-request-list-header";
    const title = element("strong", mergeRequest.title);
    const identity = element("span", `!${mergeRequest.id}`);
    identity.className = "merge-request-number";
    header.append(title, mergeRequestStateBadge(mergeRequest.state), identity);
    const branches = mergeRequestDirection(mergeRequest.target, mergeRequest.source);
    const metadata = element("div");
    metadata.className = "merge-request-list-metadata";
    const updated = element("span", `${mergeRequest.author} updated ${relativeTime(mergeRequest.updatedAt)}`);
    updated.title = new Date(mergeRequest.updatedAt).toLocaleString();
    metadata.append(branches, updated);
    const review = element("div");
    review.className = "merge-request-list-review";
    review.append(element("span", `${mergeRequest.currentApprovals}/${mergeRequest.requiredApprovals} approvals`), element("span", `${mergeRequest.unresolvedThreads} unresolved`));
    if (mergeRequest.staleApprovals > 0) {
        review.append(element("span", `${mergeRequest.staleApprovals} stale`));
    }
    if (!mergeRequest.mergeable && mergeRequest.state === "open") {
        const conflicted = element("span", "Conflicts");
        conflicted.className = "merge-request-conflict-label";
        review.append(conflicted);
    }
    link.append(header, metadata, review);
    item.append(link);
    return item;
}
async function repositoryMergeRequestList(route, compareTrigger, canWrite) {
    const data = await request(`${repositoryMergeRequestsAPIURL(route.repository)}?state=all`);
    const mergeRequests = data.mergeRequests
        .filter((mergeRequest) => mergeRequest.state === route.mergeRequestState)
        .sort((left, right) => new Date(right.updatedAt).getTime() - new Date(left.updatedAt).getTime());
    document.title = `Merge requests · ${route.repository} · GitOne`;
    const section = element("section");
    section.className = "content-section merge-request-list-view";
    const actions = [];
    if (canWrite) {
        const create = actionButton("New merge request", "git-merge", "primary");
        create.disabled = compareTrigger.disabled;
        create.title = compareTrigger.title;
        create.addEventListener("click", () => compareTrigger.click());
        actions.push(create);
    }
    section.append(sectionHeading("Merge requests", mergeRequests.length, actions));
    const filters = element("nav");
    filters.className = "merge-request-filters";
    filters.setAttribute("aria-label", "Merge request state");
    for (const state of ["open", "merged", "closed"]) {
        const count = data.mergeRequests.filter((mergeRequest) => mergeRequest.state === state).length;
        const label = state[0].toUpperCase() + state.slice(1);
        const link = element("a", `${label} ${count}`);
        link.href = mergeRequestBrowserURL(route, { state });
        if (state === route.mergeRequestState) {
            link.setAttribute("aria-current", "page");
        }
        filters.append(link);
    }
    section.append(filters);
    if (mergeRequests.length === 0) {
        section.append(emptyState(`No ${route.mergeRequestState} merge requests.`));
        return section;
    }
    const list = element("ul");
    list.className = "merge-request-list";
    for (const mergeRequest of mergeRequests) {
        list.append(mergeRequestListItem(route, mergeRequest));
    }
    section.append(list);
    return section;
}
async function mergeRequestDescription(mergeRequest) {
    const card = element("section");
    card.className = "merge-request-description";
    const header = element("header");
    const identity = element("span", mergeRequest.author.slice(0, 1).toUpperCase() || "?");
    identity.className = "commit-avatar";
    const metadata = element("div");
    const opened = element("span", `opened ${relativeTime(mergeRequest.createdAt)}`);
    opened.title = new Date(mergeRequest.createdAt).toLocaleString();
    metadata.append(element("strong", mergeRequest.author), opened);
    header.append(identity, metadata);
    card.append(header);
    if (mergeRequest.description.trim()) {
        card.append(await markdownPreview(mergeRequest.description));
    }
    else {
        const empty = element("p", "No description provided.");
        empty.className = "merge-request-empty-copy";
        card.append(empty);
    }
    return card;
}
async function mergeRequestComment(comment) {
    const card = element("article");
    card.className = "merge-request-comment";
    const header = element("header");
    const identity = element("span", comment.author.slice(0, 1).toUpperCase() || "?");
    identity.className = "commit-avatar";
    const metadata = element("div");
    const created = element("span", relativeTime(comment.createdAt));
    created.title = new Date(comment.createdAt).toLocaleString();
    metadata.append(element("strong", comment.author), created);
    header.append(identity, metadata);
    card.append(header, await markdownPreview(comment.body));
    return card;
}
function mergeRequestReplyForm(route, mergeRequest, thread) {
    const form = element("form");
    form.className = "merge-request-reply-form";
    const body = element("textarea");
    body.name = "body";
    body.required = true;
    body.rows = 3;
    body.placeholder = "Reply with Markdown…";
    const submit = actionButton("Reply", undefined, "secondary");
    submit.type = "submit";
    form.append(fieldLabel("Reply", body), submit);
    form.addEventListener("submit", async (event) => {
        event.preventDefault();
        submit.disabled = true;
        form.setAttribute("aria-busy", "true");
        try {
            await request(repositoryMergeRequestCommentsAPIURL(route.repository, mergeRequest.id, thread.id), {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ body: body.value }),
            });
            await renderRepositoryBrowser(route);
            showStatus("Reply added.");
        }
        catch (reason) {
            showStatus(reason instanceof Error ? reason.message : "Could not add the reply.", true);
            submit.disabled = false;
            form.removeAttribute("aria-busy");
        }
    });
    return form;
}
async function mergeRequestThread(route, mergeRequest, thread) {
    const section = element("section");
    section.className = thread.resolved
        ? "merge-request-thread merge-request-thread-resolved"
        : "merge-request-thread";
    const header = element("header");
    const title = element("strong", thread.resolved ? "Resolved discussion" : "Discussion");
    const metadata = element("span", relativeTime(thread.createdAt));
    metadata.title = new Date(thread.createdAt).toLocaleString();
    const heading = element("div");
    heading.append(title, metadata);
    header.append(heading);
    const threadAuthor = thread.comments[0]?.author;
    if (mergeRequest.state === "open" &&
        !mergeRequest.mergeInProgress &&
        (mergeRequest.canUpdate ||
            threadAuthor === browserSession?.username ||
            mergeRequest.author === browserSession?.username)) {
        const resolve = actionButton(thread.resolved ? "Reopen" : "Resolve", thread.resolved ? "refresh" : "check", "secondary merge-request-thread-action");
        resolve.addEventListener("click", async () => {
            resolve.disabled = true;
            try {
                await request(repositoryMergeRequestThreadsAPIURL(route.repository, mergeRequest.id, thread.id), {
                    method: "PATCH",
                    headers: { "Content-Type": "application/json" },
                    body: JSON.stringify({ resolved: !thread.resolved }),
                });
                await renderRepositoryBrowser(route);
                showStatus(thread.resolved ? "Discussion reopened." : "Discussion resolved.");
            }
            catch (reason) {
                showStatus(reason instanceof Error ? reason.message : "Could not update the discussion.", true);
                resolve.disabled = false;
            }
        });
        header.append(resolve);
    }
    section.append(header);
    const comments = element("div");
    comments.className = "merge-request-thread-comments";
    for (const comment of thread.comments) {
        comments.append(await mergeRequestComment(comment));
    }
    section.append(comments);
    if (mergeRequest.state === "open" && !mergeRequest.mergeInProgress) {
        section.append(mergeRequestReplyForm(route, mergeRequest, thread));
    }
    return section;
}
function mergeRequestNewThreadForm(route, mergeRequest) {
    const form = element("form");
    form.className = "merge-request-new-thread";
    const body = element("textarea");
    body.name = "body";
    body.required = true;
    body.rows = 5;
    body.placeholder = "Start a discussion with Markdown…";
    const submit = actionButton("Start discussion", undefined, "primary");
    submit.type = "submit";
    form.append(fieldLabel("New discussion", body), submit);
    form.addEventListener("submit", async (event) => {
        event.preventDefault();
        submit.disabled = true;
        form.setAttribute("aria-busy", "true");
        try {
            await request(repositoryMergeRequestThreadsAPIURL(route.repository, mergeRequest.id), {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ body: body.value }),
            });
            await renderRepositoryBrowser(route);
            showStatus("Discussion started.");
        }
        catch (reason) {
            showStatus(reason instanceof Error ? reason.message : "Could not start the discussion.", true);
            submit.disabled = false;
            form.removeAttribute("aria-busy");
        }
    });
    return form;
}
async function mergeRequestConversation(route, mergeRequest) {
    const conversation = element("div");
    conversation.className = "merge-request-conversation";
    conversation.append(await mergeRequestDescription(mergeRequest));
    const discussions = element("section");
    discussions.className = "merge-request-discussions";
    discussions.append(sectionHeading("Discussions", mergeRequest.threads.length));
    if (mergeRequest.threads.length === 0) {
        discussions.append(emptyState("No discussions yet."));
    }
    else {
        for (const thread of mergeRequest.threads) {
            discussions.append(await mergeRequestThread(route, mergeRequest, thread));
        }
    }
    if (mergeRequest.state === "open" && !mergeRequest.mergeInProgress) {
        discussions.append(mergeRequestNewThreadForm(route, mergeRequest));
    }
    conversation.append(discussions);
    return conversation;
}
function mergeRequestChanges(mergeRequest) {
    const changes = element("div");
    changes.className = "merge-request-changes";
    const summary = element("section");
    summary.className = "comparison-summary";
    const additions = mergeRequest.files.reduce((total, file) => total + file.additions, 0);
    const deletions = mergeRequest.files.reduce((total, file) => total + file.deletions, 0);
    const stats = element("div");
    stats.className = "comparison-stats";
    stats.append(comparisonStat("commits ahead", String(mergeRequest.ahead)), comparisonStat("behind", String(mergeRequest.behind)), comparisonStat("files changed", String(mergeRequest.files.length)), comparisonStat("additions", `+${additions}`), comparisonStat("deletions", `−${deletions}`));
    summary.append(mergeRequestDirection(mergeRequest.target, mergeRequest.source), stats);
    changes.append(summary);
    if (mergeRequest.truncated) {
        changes.append(comparisonTruncatedNotice());
    }
    const workflowReady = mergeRequest.state === "open" &&
        mergeRequest.mergeable &&
        mergeRequest.currentApprovals >= mergeRequest.requiredApprovals &&
        mergeRequest.unresolvedThreads === 0;
    let statusTitle;
    let statusDetail;
    if (mergeRequest.mergeInProgress) {
        statusTitle = "Merge in progress";
        statusDetail = "The approved source revision is being merged.";
    }
    else if (mergeRequest.state === "merged") {
        statusTitle = "Request merged";
        statusDetail = `${mergeRequest.source} was merged into ${mergeRequest.target}.`;
    }
    else if (mergeRequest.state === "closed") {
        statusTitle = "Request closed";
        statusDetail = "Reopen this request to continue its review.";
    }
    else if (!mergeRequest.mergeable) {
        statusTitle = "Merge conflicts detected";
        statusDetail = "Resolve the conflicting files in a local checkout.";
    }
    else if (mergeRequest.unresolvedThreads > 0) {
        statusTitle = "Discussions must be resolved";
        statusDetail = "Resolve every review thread before merging.";
    }
    else if (mergeRequest.currentApprovals < mergeRequest.requiredApprovals) {
        statusTitle = "Review required";
        statusDetail = "The current source revision needs approval before merging.";
    }
    else {
        statusTitle = "Request is ready to merge";
        statusDetail = `${mergeRequest.source} can be merged into ${mergeRequest.target}.`;
    }
    const status = element("section");
    status.className = workflowReady ||
        mergeRequest.state === "merged" ||
        mergeRequest.mergeInProgress
        ? "merge-status merge-ready"
        : "merge-status merge-conflicted";
    const statusCopy = element("div");
    statusCopy.append(element("strong", statusTitle), element("span", statusDetail));
    status.append(statusCopy);
    if (!mergeRequest.mergeable && mergeRequest.conflicts.length > 0) {
        const conflicts = element("ul");
        conflicts.className = "conflict-list";
        for (const path of mergeRequest.conflicts) {
            conflicts.append(element("li", path));
        }
        status.append(conflicts);
    }
    changes.append(status);
    const files = element("div");
    files.className = "comparison-files";
    if (mergeRequest.files.length === 0) {
        files.append(emptyState("No file changes in this merge request."));
    }
    else {
        for (const file of mergeRequest.files) {
            files.append(comparisonDiff(file));
        }
    }
    changes.append(files);
    return changes;
}
function mergeRequestApprovalList(mergeRequest) {
    const list = element("ul");
    list.className = "merge-request-approval-list";
    for (const approval of mergeRequest.approvals) {
        const item = element("li");
        const copy = element("div");
        const created = element("span", relativeTime(approval.createdAt));
        created.title = new Date(approval.createdAt).toLocaleString();
        copy.append(element("strong", approval.author), created);
        const state = element("span", approval.current ? "Current" : "Stale");
        state.className = approval.current
            ? "merge-request-approval-current"
            : "merge-request-approval-stale";
        item.append(icon(approval.current ? "check" : "clock"), copy, state);
        list.append(item);
    }
    return list;
}
function mergeRequestReviewPanel(route, mergeRequest) {
    const sidebar = element("aside");
    sidebar.className = "merge-request-sidebar";
    const review = element("section");
    review.className = "merge-request-panel";
    review.append(element("h3", "Review status"));
    const approvalSummary = element("strong", `${mergeRequest.currentApprovals} of ${mergeRequest.requiredApprovals} approvals`);
    const approvalDetail = element("p", mergeRequest.currentApprovals >= mergeRequest.requiredApprovals
        ? "The current source revision is approved."
        : "Approval is required for the current source revision.");
    review.append(approvalSummary, approvalDetail);
    if (mergeRequest.staleApprovals > 0) {
        const stale = element("p", `${mergeRequest.staleApprovals} approval${mergeRequest.staleApprovals === 1 ? "" : "s"} became stale after the source changed.`);
        stale.className = "merge-request-stale-copy";
        review.append(stale);
    }
    if (mergeRequest.approvals.length > 0) {
        review.append(mergeRequestApprovalList(mergeRequest));
    }
    if (mergeRequest.state === "open") {
        if (mergeRequest.mergeInProgress) {
            review.append(element("p", "Merge in progress…"));
            const refresh = actionButton("Refresh status", "refresh", "secondary");
            refresh.addEventListener("click", async () => {
                refresh.disabled = true;
                review.setAttribute("aria-busy", "true");
                try {
                    await renderRepositoryBrowser(route);
                }
                catch (reason) {
                    showStatus(reason instanceof Error ? reason.message : "Could not refresh the request.", true);
                    refresh.disabled = false;
                    review.removeAttribute("aria-busy");
                }
            });
            review.append(refresh);
        }
        else if (mergeRequest.canMerge &&
            mergeRequest.currentApprovals >= mergeRequest.requiredApprovals) {
            const merge = actionButton("Retry merge", "git-merge", "primary");
            merge.addEventListener("click", async () => {
                merge.disabled = true;
                review.setAttribute("aria-busy", "true");
                try {
                    const updated = await request(repositoryMergeRequestMergeAPIURL(route.repository, mergeRequest.id), {
                        method: "POST",
                        headers: { "Content-Type": "application/json" },
                        body: JSON.stringify({
                            expectedHeadCommit: mergeRequest.headCommit,
                        }),
                    });
                    await showUpdatedMergeRequest(route, updated, updated.state === "merged"
                        ? `Merge request !${updated.id} merged.`
                        : `Merge request !${updated.id} remains open.`);
                }
                catch (reason) {
                    showStatus(reason instanceof Error ? reason.message : "Could not merge the request.", true);
                    merge.disabled = false;
                    review.removeAttribute("aria-busy");
                }
            });
            review.append(merge);
        }
        else if (mergeRequest.canApprove && !mergeRequest.viewerApproved) {
            const approvalWillMerge = mergeRequest.mergeable &&
                mergeRequest.unresolvedThreads === 0 &&
                mergeRequest.currentApprovals + 1 >= mergeRequest.requiredApprovals;
            const approve = actionButton(approvalWillMerge ? "Approve and merge" : "Approve", "check", "primary");
            approve.addEventListener("click", async () => {
                approve.disabled = true;
                review.setAttribute("aria-busy", "true");
                try {
                    const updated = await request(repositoryMergeRequestApprovalsAPIURL(route.repository, mergeRequest.id), {
                        method: "POST",
                        headers: { "Content-Type": "application/json" },
                        body: JSON.stringify({
                            expectedHeadCommit: mergeRequest.headCommit,
                        }),
                    });
                    await showUpdatedMergeRequest(route, updated, updated.state === "merged"
                        ? `Merge request !${updated.id} approved and merged.`
                        : `Merge request !${updated.id} approved.`);
                }
                catch (reason) {
                    showStatus(reason instanceof Error ? reason.message : "Could not approve the request.", true);
                    approve.disabled = false;
                    review.removeAttribute("aria-busy");
                }
            });
            review.append(approve);
        }
        else if (mergeRequest.viewerApproved) {
            const approved = element("p", "You approved the current revision.");
            approved.className = "merge-request-viewer-approved";
            review.append(approved);
        }
        if (!mergeRequest.mergeable &&
            mergeRequest.currentApprovals >= mergeRequest.requiredApprovals) {
            const blocked = element("p", "Approval is recorded, but conflicts must be resolved before merging.");
            blocked.className = "merge-request-blocked-copy";
            review.append(blocked);
        }
    }
    sidebar.append(review);
    const details = element("section");
    details.className = "merge-request-panel";
    details.append(element("h3", "Details"));
    const definition = element("dl");
    const sourceCommit = element("code", shortCommitHash(mergeRequest.headCommit));
    sourceCommit.title = mergeRequest.headCommit;
    const targetCommit = element("code", shortCommitHash(mergeRequest.targetCommit));
    targetCommit.title = mergeRequest.targetCommit;
    definition.append(element("dt", "Source"), element("dd", mergeRequest.source), element("dt", "Target"), element("dd", mergeRequest.target), element("dt", "Source commit"), element("dd"), element("dt", "Target commit"), element("dd"), element("dt", "Unresolved"), element("dd", String(mergeRequest.unresolvedThreads)));
    definition.children[5]?.append(sourceCommit);
    definition.children[7]?.append(targetCommit);
    details.append(definition);
    if (mergeRequest.state === "merged" && mergeRequest.mergedCommit) {
        const merged = element("p", `Merged by ${mergeRequest.mergedBy ?? "unknown"} at ${shortCommitHash(mergeRequest.mergedCommit)}.`);
        if (mergeRequest.mergedAt) {
            merged.append(document.createTextNode(` ${relativeTime(mergeRequest.mergedAt)}.`));
            merged.title = new Date(mergeRequest.mergedAt).toLocaleString();
        }
        details.append(merged);
    }
    else if (mergeRequest.state === "closed" && mergeRequest.closedAt) {
        const closed = element("p", `Closed by ${mergeRequest.closedBy ?? "unknown"} ${relativeTime(mergeRequest.closedAt)}.`);
        closed.title = new Date(mergeRequest.closedAt).toLocaleString();
        details.append(closed);
    }
    if (mergeRequest.canUpdate && mergeRequest.state !== "merged") {
        const nextState = mergeRequest.state === "closed" ? "open" : "closed";
        const update = actionButton(nextState === "open" ? "Reopen request" : "Close request", nextState === "open" ? "refresh" : "close", nextState === "open" ? "primary" : "danger-secondary");
        update.addEventListener("click", async () => {
            update.disabled = true;
            details.setAttribute("aria-busy", "true");
            try {
                const updated = await request(repositoryMergeRequestsAPIURL(route.repository, mergeRequest.id), {
                    method: "PATCH",
                    headers: { "Content-Type": "application/json" },
                    body: JSON.stringify({ state: nextState }),
                });
                await showUpdatedMergeRequest(route, updated, nextState === "open"
                    ? `Merge request !${updated.id} reopened.`
                    : `Merge request !${updated.id} closed.`);
            }
            catch (reason) {
                showStatus(reason instanceof Error ? reason.message : "Could not update the request.", true);
                update.disabled = false;
                details.removeAttribute("aria-busy");
            }
        });
        details.append(update);
    }
    sidebar.append(details);
    return sidebar;
}
async function repositoryMergeRequestDetail(route, mergeRequest) {
    document.title = `!${mergeRequest.id} ${mergeRequest.title} · ${route.repository} · GitOne`;
    const section = element("section");
    section.className = "content-section merge-request-detail";
    const header = element("header");
    header.className = "merge-request-detail-header";
    const heading = element("div");
    const title = element("h2", mergeRequest.title);
    const identity = element("span", `!${mergeRequest.id}`);
    identity.className = "merge-request-number";
    heading.append(title, identity, mergeRequestStateBadge(mergeRequest.state));
    const opened = element("p", `${mergeRequest.author} opened this request ${relativeTime(mergeRequest.createdAt)}`);
    opened.title = new Date(mergeRequest.createdAt).toLocaleString();
    header.append(heading, opened, mergeRequestDirection(mergeRequest.target, mergeRequest.source));
    const tabs = element("nav");
    tabs.className = "merge-request-tabs";
    tabs.setAttribute("aria-label", "Merge request");
    for (const tab of ["conversation", "changes"]) {
        const label = tab === "conversation"
            ? `Conversation ${mergeRequest.threads.length}`
            : `Changes ${mergeRequest.files.length}`;
        const link = element("a", label);
        link.href = mergeRequestBrowserURL(route, {
            mergeRequest: mergeRequest.id,
            tab,
            state: route.mergeRequestState,
        });
        if (route.reviewTab === tab) {
            link.setAttribute("aria-current", "page");
        }
        tabs.append(link);
    }
    const layout = element("div");
    layout.className = "merge-request-detail-layout";
    const main = route.reviewTab === "changes"
        ? mergeRequestChanges(mergeRequest)
        : await mergeRequestConversation(route, mergeRequest);
    layout.append(main, mergeRequestReviewPanel(route, mergeRequest));
    section.append(header, tabs, layout);
    return section;
}
async function repositoryMergeRequestsView(route, compareTrigger, canWrite) {
    if (route.mergeRequest === undefined) {
        return await repositoryMergeRequestList(route, compareTrigger, canWrite);
    }
    const mergeRequest = await request(repositoryMergeRequestsAPIURL(route.repository, route.mergeRequest));
    return await repositoryMergeRequestDetail(route, mergeRequest);
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
function repositoryArchiveControl(route) {
    const trigger = actionButton("Download", "download", "secondary");
    const dialog = element("dialog");
    dialog.className = "action-dialog archive-dialog";
    const content = element("div");
    content.className = "dialog-form";
    const header = element("div");
    header.className = "dialog-header";
    const title = element("h2", "Download repository");
    title.id = "archive-dialog-title";
    dialog.setAttribute("aria-labelledby", title.id);
    const close = actionButton("Close", "close", "icon-button");
    close.setAttribute("aria-label", "Close");
    close.title = "Close";
    header.append(title, close);
    const description = element("p", `Download ${route.repository} at ${route.ref}, without its Git history.`);
    description.className = "dialog-description";
    const options = element("div");
    options.className = "archive-options";
    for (const [format, label] of [
        ["zip", "Download ZIP"],
        ["tar.gz", "Download tar.gz"],
    ]) {
        const link = element("a");
        link.className = "button secondary";
        link.href = repositoryArchiveAPIURL(route.repository, route.ref, format);
        link.append(icon("download"), document.createTextNode(label));
        options.append(link);
    }
    content.append(header, description, options);
    dialog.append(content);
    trigger.addEventListener("click", () => dialog.showModal());
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
function repositoryFileCreator(route, tree) {
    const trigger = actionButton("New file", "plus", "primary");
    const dialog = element("dialog");
    dialog.className = "action-dialog file-create-dialog";
    const form = element("form");
    form.className = "dialog-form";
    const header = element("div");
    header.className = "dialog-header";
    const title = element("h2", "Create file");
    const close = actionButton("Close", "close", "icon-button");
    close.setAttribute("aria-label", "Close");
    close.title = "Close";
    header.append(title, close);
    const path = element("input");
    path.name = "path";
    path.required = true;
    path.autocomplete = "off";
    path.spellcheck = false;
    path.value = tree.path ? `${tree.path}/` : "";
    path.placeholder = tree.path ? `${tree.path}/example.txt` : "path/to/example.txt";
    const contents = element("textarea");
    contents.name = "content";
    contents.rows = 14;
    contents.maxLength = 1_048_576;
    contents.spellcheck = false;
    contents.className = "file-create-content";
    const message = element("input");
    message.name = "message";
    message.maxLength = 500;
    message.placeholder = "Defaults to “Create <path>”";
    const actions = element("div");
    actions.className = "dialog-actions";
    const cancel = actionButton("Cancel", undefined, "secondary");
    const submit = actionButton("Create file", "plus", "primary");
    submit.type = "submit";
    actions.append(cancel, submit);
    form.append(header, fieldLabel("File path", path), fieldLabel("File contents", contents), fieldLabel("Commit message", message), actions);
    dialog.append(form);
    trigger.addEventListener("click", () => {
        dialog.showModal();
        path.focus();
        path.setSelectionRange(path.value.length, path.value.length);
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
        submit.disabled = true;
        cancel.disabled = true;
        form.setAttribute("aria-busy", "true");
        try {
            const created = await request(repositoryFileAPIURL(route.repository, route.ref, path.value), {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({
                    content: contents.value,
                    message: message.value,
                    expectedCommit: tree.commit,
                }),
            });
            dialog.close();
            const nextRoute = {
                repository: route.repository,
                ref: created.branch,
                path: "",
                file: created.path,
                view: "files",
                page: 1,
                reviewTab: "conversation",
                mergeRequestState: "open",
            };
            window.history.pushState(null, "", repositoryBrowserURL(route.repository, {
                ref: created.branch,
                file: created.path,
            }));
            await renderRepositoryBrowser(nextRoute);
            showStatus(`${created.path} created on ${created.branch} at ${shortCommitHash(created.commit)}.`);
        }
        catch (reason) {
            showStatus(reason instanceof Error ? reason.message : "Could not create the file.", true);
            submit.disabled = false;
            cancel.disabled = false;
            form.removeAttribute("aria-busy");
        }
    });
    return { trigger, dialog };
}
function repositoryFileRenameControl(route, content) {
    const trigger = actionButton("Rename file", "pencil", "secondary");
    const dialog = element("dialog");
    dialog.className = "action-dialog";
    const form = element("form");
    form.className = "dialog-form";
    const header = element("div");
    header.className = "dialog-header";
    const title = element("h2", "Rename file");
    const close = actionButton("Close", "close", "icon-button");
    close.setAttribute("aria-label", "Close");
    close.title = "Close";
    header.append(title, close);
    const path = element("input");
    path.name = "newPath";
    path.required = true;
    path.autocomplete = "off";
    path.spellcheck = false;
    path.value = content.path;
    const message = element("input");
    message.name = "message";
    message.maxLength = 500;
    message.placeholder = "Defaults to a rename message";
    const actions = element("div");
    actions.className = "dialog-actions";
    const cancel = actionButton("Cancel", undefined, "secondary");
    const submit = actionButton("Rename file", "pencil", "primary");
    submit.type = "submit";
    actions.append(cancel, submit);
    form.append(header, fieldLabel("New file path", path), fieldLabel("Commit message", message), actions);
    dialog.append(form);
    trigger.addEventListener("click", () => {
        dialog.showModal();
        path.focus();
        path.select();
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
        if (path.value === content.path) {
            path.setCustomValidity("Choose a different file path.");
            path.reportValidity();
            return;
        }
        path.setCustomValidity("");
        submit.disabled = true;
        cancel.disabled = true;
        form.setAttribute("aria-busy", "true");
        try {
            const renamed = await request(repositoryFileAPIURL(route.repository, route.ref, content.path), {
                method: "PATCH",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({
                    newPath: path.value,
                    message: message.value,
                    expectedCommit: content.commit,
                }),
            });
            dialog.close();
            const nextRoute = {
                repository: route.repository,
                ref: renamed.branch,
                path: "",
                file: renamed.path,
                view: "files",
                page: 1,
                reviewTab: "conversation",
                mergeRequestState: "open",
            };
            window.history.pushState(null, "", repositoryBrowserURL(route.repository, {
                ref: renamed.branch,
                file: renamed.path,
            }));
            await renderRepositoryBrowser(nextRoute);
            showStatus(`${renamed.previousPath ?? content.path} renamed to ${renamed.path} at ${shortCommitHash(renamed.commit)}.`);
        }
        catch (reason) {
            showStatus(reason instanceof Error ? reason.message : "Could not rename the file.", true);
            submit.disabled = false;
            cancel.disabled = false;
            form.removeAttribute("aria-busy");
        }
    });
    return { trigger, dialog };
}
function repositoryFileDeleteControl(route, content) {
    const trigger = actionButton("Delete file", "trash", "danger-secondary");
    const dialog = element("dialog");
    dialog.className = "action-dialog";
    const form = element("form");
    form.className = "dialog-form";
    const header = element("div");
    header.className = "dialog-header";
    const title = element("h2", "Delete file");
    const close = actionButton("Close", "close", "icon-button");
    close.setAttribute("aria-label", "Close");
    close.title = "Close";
    header.append(title, close);
    const warning = element("p", `Delete ${content.path} from ${route.ref}? This creates a new commit.`);
    const message = element("input");
    message.name = "message";
    message.maxLength = 500;
    message.placeholder = `Defaults to “Delete ${content.path}”`;
    const actions = element("div");
    actions.className = "dialog-actions";
    const cancel = actionButton("Cancel", undefined, "secondary");
    const submit = actionButton("Delete file", "trash", "danger");
    submit.type = "submit";
    actions.append(cancel, submit);
    form.append(header, warning, fieldLabel("Commit message", message), actions);
    dialog.append(form);
    trigger.addEventListener("click", () => {
        dialog.showModal();
        message.focus();
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
        submit.disabled = true;
        cancel.disabled = true;
        form.setAttribute("aria-busy", "true");
        try {
            const deleted = await request(repositoryFileAPIURL(route.repository, route.ref, content.path), {
                method: "DELETE",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({
                    message: message.value,
                    expectedCommit: content.commit,
                }),
            });
            dialog.close();
            const parentPath = content.path.split("/").slice(0, -1).join("/");
            const nextRoute = {
                repository: route.repository,
                ref: deleted.branch,
                path: parentPath,
                file: null,
                view: "files",
                page: 1,
                reviewTab: "conversation",
                mergeRequestState: "open",
            };
            window.history.pushState(null, "", repositoryBrowserURL(route.repository, {
                ref: deleted.branch,
                path: parentPath,
            }));
            await renderRepositoryBrowser(nextRoute);
            showStatus(`${deleted.path} deleted from ${deleted.branch} at ${shortCommitHash(deleted.commit)}.`);
        }
        catch (reason) {
            showStatus(reason instanceof Error ? reason.message : "Could not delete the file.", true);
            submit.disabled = false;
            cancel.disabled = false;
            form.removeAttribute("aria-busy");
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
    if (builds !== null) {
        metadata.append(latestBranchBuildIndicator(route, builds));
    }
    metadata.append(hash);
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
function repositoryBlameSection(route, blame) {
    const section = element("section");
    section.className = "content-section blame-view";
    const fileLink = element("a");
    fileLink.className = "button secondary";
    fileLink.href = repositoryBrowserURL(route.repository, {
        ref: route.ref,
        file: blame.path,
    });
    fileLink.append(icon("file"), document.createTextNode("View file"));
    section.append(sectionHeading(blame.path, undefined, [fileLink]));
    const metadata = element("p", `${blame.lines.length} ${blame.lines.length === 1 ? "line" : "lines"} · ${shortCommitHash(blame.commit)}`);
    metadata.className = "file-metadata";
    section.append(metadata);
    if (blame.lines.length === 0) {
        section.append(emptyState("This file is empty."));
        return section;
    }
    const table = element("table");
    table.className = "repository-blame";
    const body = element("tbody");
    for (let index = 0; index < blame.lines.length;) {
        const first = blame.lines[index];
        let groupEnd = index + 1;
        while (groupEnd < blame.lines.length &&
            blame.lines[groupEnd].commit === first.commit) {
            groupEnd++;
        }
        for (let lineIndex = index; lineIndex < groupEnd; lineIndex++) {
            const line = blame.lines[lineIndex];
            const row = element("tr");
            if (lineIndex === index) {
                const attribution = element("td");
                attribution.className = "blame-attribution";
                attribution.rowSpan = groupEnd - index;
                const author = element("strong", first.author);
                author.title = first.email;
                const commit = element("code", shortCommitHash(first.commit));
                commit.title = first.commit;
                const authored = element("span", relativeTime(first.authored));
                authored.title = new Date(first.authored).toLocaleString();
                attribution.append(author, commit, authored);
                row.append(attribution);
            }
            const number = element("th", String(line.number));
            number.className = "blame-line-number";
            number.scope = "row";
            const source = element("td");
            source.className = "blame-line";
            source.append(element("code", line.text || " "));
            row.append(number, source);
            body.append(row);
        }
        index = groupEnd;
    }
    table.append(body);
    section.append(table);
    return section;
}
function repositoryFileActionMenu(controls) {
    if (controls.length === 0) {
        return null;
    }
    const menu = element("details");
    menu.className = "file-action-menu";
    const summary = element("summary");
    summary.className = "button secondary icon-button";
    summary.setAttribute("aria-label", "More file actions");
    summary.title = "More file actions";
    summary.append(icon("ellipsis"));
    const panel = element("div");
    panel.className = "file-action-menu-panel";
    for (const control of controls) {
        panel.append(control.trigger);
        control.trigger.addEventListener("click", () => {
            menu.open = false;
        }, { capture: true });
        control.dialog.addEventListener("close", () => {
            menu.open = false;
            if (summary.isConnected) {
                summary.focus();
            }
        });
    }
    menu.addEventListener("toggle", () => {
        if (menu.open) {
            if (openFileActionMenu && openFileActionMenu !== menu) {
                openFileActionMenu.open = false;
            }
            openFileActionMenu = menu;
        }
        else if (openFileActionMenu === menu) {
            openFileActionMenu = null;
        }
    });
    menu.addEventListener("keydown", (event) => {
        if (event.key === "Escape" && menu.open) {
            event.preventDefault();
            menu.open = false;
            summary.focus();
        }
    });
    menu.append(summary, panel);
    return menu;
}
async function repositoryBlobSection(route, content) {
    const section = element("section");
    section.className = "content-section file-view";
    const editButton = content.canEdit
        ? actionButton("Edit", "pencil", "primary")
        : null;
    const renameControl = content.canManage
        ? repositoryFileRenameControl(route, content)
        : null;
    const deleteControl = content.canManage
        ? repositoryFileDeleteControl(route, content)
        : null;
    const blameLink = content.encoding === "utf-8" &&
        !content.lfs &&
        content.size <= 1_048_576
        ? element("a")
        : null;
    if (blameLink) {
        blameLink.className = "button secondary";
        blameLink.href = repositoryBrowserURL(route.repository, {
            ref: route.ref,
            file: content.path,
            view: "blame",
        });
        blameLink.append(icon("clock"), document.createTextNode("Blame"));
    }
    const fileActionMenu = repositoryFileActionMenu([
        renameControl,
        deleteControl,
    ].filter((control) => control !== null));
    const fileActionControls = [
        blameLink,
        editButton,
        fileActionMenu,
    ].filter((action) => action !== null);
    const fileActions = element("div");
    fileActions.className = "file-actions";
    fileActions.append(...fileActionControls);
    const heading = sectionHeading(content.path, undefined, fileActionControls.length > 0 ? [fileActions] : []);
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
    if (renameControl) {
        section.append(renameControl.dialog);
    }
    if (deleteControl) {
        section.append(deleteControl.dialog);
    }
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
        fileActionMenu?.removeAttribute("open");
        fileActions.hidden = true;
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
            fileActions.hidden = false;
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
function renderEmptyRepositoryBrowser(route, branches, group) {
    const repositoryParts = route.repository.split("/");
    const repositoryName = repositoryParts.at(-1) ?? route.repository;
    const groupPath = repositoryParts.slice(0, -1).join("/");
    const repository = group.repositories.find((candidate) => candidate.name === repositoryName);
    document.title = `${route.repository} · GitOne`;
    app.replaceChildren();
    const repositoryDescription = element("p");
    repositoryDescription.className = "repository-page-description";
    repositoryDescription.append(element("strong", repositoryName));
    const description = repository?.description.trim();
    if (description) {
        repositoryDescription.append(document.createTextNode(` — ${description}`));
    }
    const branchCreator = repositoryBranchCreator(route, branches);
    const branchComparison = repositoryBranchComparison(route, branches);
    const branchManager = repositoryBranchManager(route, branches);
    const clone = cloneControl(repositoryURL(groupPath, repositoryName, group.username));
    const rename = group.role === "maintainer" || group.role === "owner"
        ? repositoryRenameControl(route, groupPath, repositoryName)
        : null;
    const branchLabel = element("label");
    const branchLabelText = element("span");
    branchLabelText.append(icon("git-branch"), document.createTextNode("Branch"));
    const branchSelect = element("select");
    branchSelect.disabled = true;
    branchSelect.append(element("option", "No branches"));
    branchLabel.append(branchLabelText, branchSelect);
    const branchPicker = element("div");
    branchPicker.className = "branch-picker";
    branchPicker.append(branchLabel, branchManager.trigger, branchCreator.trigger, branchComparison.trigger);
    const branchControl = element("div");
    branchControl.className = "branch-control";
    branchControl.append(branchPicker);
    const repositoryActions = element("div");
    repositoryActions.className = "repository-actions";
    repositoryActions.append(...(rename ? [rename.trigger] : []), clone.trigger);
    const toolbar = element("div");
    toolbar.className = "repository-toolbar";
    toolbar.append(branchControl, repositoryActions);
    const overview = element("section");
    overview.className = "repository-overview";
    overview.append(toolbar);
    const content = element("section");
    content.className = "content-section";
    content.append(emptyState("This repository has no browsable default. Push a commit to create its first branch."));
    app.append(repositoryDescription, overview, repositoryNavigation(route), branchCreator.dialog, branchComparison.dialog, branchManager.dialog, clone.dialog, ...(rename ? [rename.dialog] : []), content);
}
async function renderRepositoryBrowser(route) {
    stopRepositoryBuildPolling();
    const repositoryParts = route.repository.split("/");
    const repositoryName = repositoryParts.at(-1) ?? route.repository;
    const groupPath = repositoryParts.slice(0, -1).join("/");
    const [branches, group] = await Promise.all([
        request(repositoryBranchesAPIURL(route.repository)),
        request(apiGroupURL(groupPath)).catch(() => ({
            path: groupPath,
            description: "",
            username: "",
            role: "read",
            subgroups: [],
            repositories: [{ name: repositoryName, description: "" }],
        })),
    ]);
    if (!route.ref) {
        route = { ...route, ref: branches.defaultRef };
    }
    setRepositoryLocation(route);
    if (!route.ref) {
        renderEmptyRepositoryBrowser(route, branches, group);
        return;
    }
    const commitParameters = new URLSearchParams({
        page: String(route.view === "history" ? route.page : 1),
        perPage: String(route.view === "history" ? 50 : 1),
    });
    const commitsRequest = request(`${repositoryAPIURL(route.repository, "commits", route.ref)}?${commitParameters}`);
    const contentRequest = route.view === "history" ||
        route.view === "builds" ||
        route.view === "merge-requests"
        ? Promise.resolve(null)
        : route.view === "blame" && route.file !== null
            ? request(repositoryAPIURL(route.repository, "blame", route.ref, route.file))
            : route.file === null
                ? request(repositoryAPIURL(route.repository, "tree", route.ref, route.path))
                : request(repositoryAPIURL(route.repository, "blob", route.ref, route.file));
    const buildsRequest = route.view === "builds" || (route.view === "files" && route.file === null)
        ? request(repositoryBuildsAPIURL(route.repository)).catch((reason) => {
            if (reason instanceof RequestError && (reason.status === 401 || reason.status === 403)) {
                return null;
            }
            throw reason;
        })
        : Promise.resolve(null);
    const [commits, content, builds] = await Promise.all([
        commitsRequest,
        contentRequest,
        buildsRequest,
    ]);
    const repository = group.repositories.find((candidate) => candidate.name === repositoryName);
    document.title = `${route.repository} · GitOne`;
    app.replaceChildren();
    const description = repository?.description.trim();
    const repositoryDescription = element("p");
    repositoryDescription.className = "repository-page-description";
    repositoryDescription.append(element("strong", repositoryName));
    if (description) {
        repositoryDescription.append(document.createTextNode(` — ${description}`));
    }
    app.append(repositoryDescription);
    const overview = element("section");
    overview.className = "repository-overview";
    const branchCreator = repositoryBranchCreator(route, branches);
    const branchComparison = repositoryBranchComparison(route, branches);
    const branchManager = repositoryBranchManager(route, branches);
    const clone = cloneControl(repositoryURL(groupPath, repositoryName, group.username));
    const archive = repositoryArchiveControl(route);
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
        const option = element("option", branch.name === branches.defaultBranch
            ? `${branch.name} (default)`
            : branch.name);
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
            mergeRequest: route.mergeRequest,
            reviewTab: route.reviewTab,
            mergeRequestState: route.mergeRequestState,
        });
    });
    branchLabel.append(branchSelect);
    const branchPicker = element("div");
    branchPicker.className = "branch-picker";
    branchPicker.append(branchLabel, branchManager.trigger, branchCreator.trigger, branchComparison.trigger);
    branchControl.append(branchPicker);
    const commitHash = content?.commit
        ?? branches.branches.find((branch) => branch.name === route.ref)?.commit;
    if (commitHash) {
        const commit = element("div");
        commit.className = "current-commit";
        commit.append(element("span", "Commit"), element("code", shortCommitHash(commitHash)));
        if (commits.total !== undefined) {
            const total = element("span", `${commits.total} ${commits.total === 1 ? "commit" : "commits"}`);
            total.className = "current-commit-total";
            commit.append(total);
        }
        branchControl.append(commit);
    }
    const repositoryActions = element("div");
    repositoryActions.className = "repository-actions";
    const setDefault = repositoryDefaultBranchControl(route, branches);
    const rename = group.role === "maintainer" || group.role === "owner"
        ? repositoryRenameControl(route, groupPath, repositoryName)
        : null;
    repositoryActions.append(...(setDefault ? [setDefault] : []), ...(rename ? [rename.trigger] : []), archive.trigger, clone.trigger);
    toolbar.append(branchControl, repositoryActions);
    overview.append(toolbar);
    const fileCreator = content !== null && "entries" in content && content.canEdit
        ? repositoryFileCreator(route, content)
        : null;
    app.append(overview, repositoryNavigation(route, fileCreator?.trigger), branchCreator.dialog, branchComparison.dialog, branchManager.dialog, archive.dialog, clone.dialog, ...(rename ? [rename.dialog] : []), ...(fileCreator ? [fileCreator.dialog] : []));
    if (route.view === "history") {
        app.append(repositoryHistory(route, commits));
        return;
    }
    if (route.view === "builds") {
        if (builds === null) {
            app.append(emptyState("Builds are available to group members."));
            return;
        }
        app.append(repositoryBuildsView(route, builds));
        return;
    }
    if (route.view === "merge-requests") {
        app.append(await repositoryMergeRequestsView(route, branchComparison.trigger, branches.canWrite));
        return;
    }
    if (route.view === "blame") {
        if (content === null || !("lines" in content)) {
            throw new Error("Repository blame is unavailable.");
        }
        app.append(repositoryBlameSection(route, content));
        return;
    }
    if (content === null) {
        throw new Error("Repository contents are unavailable.");
    }
    if ("lines" in content) {
        throw new Error("Repository blame was returned outside the blame view.");
    }
    if ("entries" in content) {
        const section = element("section");
        section.className = "content-section";
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
function setBrowserSession(session, loginShell = session === null) {
    browserSession = session;
    globalNavigation.hidden = session === null;
    sessionControls.hidden = session === null;
    sessionUsername.textContent = session?.username ?? "";
    app.classList.toggle("login-shell", loginShell);
}
function renderLogin(message = "") {
    stopRepositoryBuildPolling();
    setLocationContext([]);
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
    username.placeholder = "alice@example.com";
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
    form.append(fieldLabel("Full LDAP identity", username), fieldLabel("Password", password), error, submit);
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
                ? "Invalid LDAP identity or password."
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
    setLocationContext([rootLocationContextItem]);
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
    setGroupLocation(path);
    const data = await request(apiGroupURL(path));
    const isRootGroup = !data.path.includes("/");
    const canManageGroup = data.role === "maintainer" || data.role === "owner";
    const controlSettings = canManageGroup && isRootGroup
        ? await request(groupSettingsAPIURL(path)).catch(() => null)
        : null;
    document.title = `${data.path} · GitOne`;
    app.replaceChildren();
    const createSubgroup = createForm("New subgroup", "Subgroup name", "backend", "New subgroup", async (name) => {
        await request(apiGroupURL(`${data.path}/${name}`), { method: "POST" });
        await renderGroup(data.path, "Subgroup created.");
    });
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
    const importRepository = repositoryImportControl(data.path);
    const settingsControl = controlSettings
        ? groupSettingsControl(data.path, controlSettings, data.role)
        : null;
    const renameControl = canManageGroup && !isRootGroup
        ? subgroupRenameControl(data.path)
        : null;
    const subgroups = element("section");
    subgroups.className = "content-section";
    subgroups.append(sectionHeading("Subgroups", data.subgroups.length), groupList(data.subgroups, "No subgroups yet.", false));
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
        ...(renameControl ? [renameControl.trigger] : []),
        ...(canManageGroup
            ? [
                createSubgroup.trigger,
                createRepository.trigger,
                importRepository.trigger,
            ]
            : []),
    ];
    app.append(pageHeader("Group", data.path, data.description, pageActions, [groupRoleBadge(data.role)]), subgroups, repositories, ...(canManageGroup
        ? [
            danger,
            createSubgroup.dialog,
            createRepository.dialog,
            importRepository.dialog,
        ]
        : []), ...(settingsControl ? [settingsControl.dialog] : []), ...(renameControl ? [renameControl.dialog] : []));
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
        const repository = currentRepository();
        const group = currentGroup();
        let session;
        try {
            session = await request("/api/session");
        }
        catch (reason) {
            if (reason instanceof RequestError && reason.status === 401) {
                if (repository !== null) {
                    setBrowserSession(null, false);
                    await renderRepositoryBrowser(repository);
                    return;
                }
                if (group !== null) {
                    setBrowserSession(null, false);
                    try {
                        await renderGroup(group);
                    }
                    catch (groupReason) {
                        if (groupReason instanceof RequestError && groupReason.status === 401) {
                            renderLogin();
                            return;
                        }
                        throw groupReason;
                    }
                    return;
                }
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
window.addEventListener("popstate", () => {
    void render();
});
void render();
