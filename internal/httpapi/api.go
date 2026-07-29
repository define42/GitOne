package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/define42/GitOne/internal/auth"
	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/review"
	"github.com/define42/GitOne/internal/runner"
	"github.com/define42/GitOne/internal/storage"
)

type API struct {
	Storage     storage.Store
	Resolver    *auth.Resolver
	Sessions    *auth.SessionManager
	Builds      *runner.Store
	Reviews     *review.Store
	Scheduler   runner.Scheduler
	Coordinator *runner.Coordinator
	RunnerToken string
}

type AuthInput struct {
	Authorization string `header:"Authorization" hidden:"true"`
	Cookie        string `header:"Cookie" hidden:"true"`
}

type healthOutput struct {
	Body struct {
		Status string `json:"status" example:"ok"`
	}
}

type groupSummary struct {
	Name        string       `json:"name"`
	Path        string       `json:"path"`
	Description string       `json:"description"`
	Role        control.Role `json:"role"`
}

type listGroupsInput struct {
	AuthInput
}

type listGroupsOutput struct {
	Body struct {
		Groups []groupSummary `json:"groups"`
	}
}

type GroupPathInput struct {
	AuthInput
	Path string `path:"path" doc:"URL-encoded full group path"`
}

type groupDetailOutput struct {
	Body struct {
		Path         string              `json:"path"`
		Description  string              `json:"description"`
		Username     string              `json:"username"`
		Subgroups    []groupSummary      `json:"subgroups"`
		Repositories []repositorySummary `json:"repositories"`
	}
}

type groupSettingsOutput struct {
	Body control.Document
}

type updateGroupSettingsBody struct {
	Name         string                              `json:"name" minLength:"1"`
	Description  string                              `json:"description"`
	Inherit      bool                                `json:"inherit"`
	Members      map[string]control.Role             `json:"members"`
	Tokens       []groupTokenInput                   `json:"tokens"`
	Repositories map[string]control.RepositoryPolicy `json:"repositories"`
}

type groupTokenInput struct {
	Name         string       `json:"name"`
	Key          string       `json:"key"`
	Hash         string       `json:"hash,omitempty"`
	NewSecret    string       `json:"newSecret,omitempty"`
	Role         control.Role `json:"role"`
	Repositories []string     `json:"repositories,omitempty"`
	ExpiresAt    *time.Time   `json:"expiresAt,omitempty"`
	Disabled     bool         `json:"disabled,omitempty"`
}

type updateGroupSettingsInput struct {
	GroupPathInput
	Body updateGroupSettingsBody
}

type updateGroupSettingsOutput struct {
	Body struct {
		Path     string           `json:"path"`
		Settings control.Document `json:"settings"`
	}
}

type repositorySummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type createGroupOutput struct {
	Body struct {
		Path string `json:"path"`
	}
}

type createGroupInput struct {
	GroupPathInput
	Description string `query:"description" doc:"Group description stored in control.json"`
}

type renameGroupBody struct {
	NewPath string `json:"newPath" minLength:"1"`
}

type renameGroupInput struct {
	GroupPathInput
	Body renameGroupBody
}

type createRepositoryOutput struct {
	Body struct {
		Group string `json:"group"`
		Name  string `json:"name"`
	}
}

type RepositoryPathInput struct {
	AuthInput
	Path string `path:"path" doc:"URL-encoded group and repository path"`
}

type createRepositoryInput struct {
	RepositoryPathInput
	InitializeReadme bool   `query:"initializeReadme" default:"false" doc:"Create an initial main commit containing README.md"`
	Description      string `query:"description" doc:"Repository description stored in .gitone.yaml"`
}

type importRepositoryBody struct {
	URL      string `json:"url" minLength:"1" doc:"HTTP or HTTPS Git remote URL"`
	Username string `json:"username,omitempty" doc:"Optional HTTP Basic authentication username"`
	Password string `json:"password,omitempty" doc:"Optional HTTP Basic password or access token"`
}

type importRepositoryInput struct {
	RepositoryPathInput
	Body importRepositoryBody
}

type renameRepositoryBody struct {
	NewName string `json:"newName" minLength:"1"`
}

type renameRepositoryInput struct {
	RepositoryPathInput
	Body renameRepositoryBody
}

type emptyOutput struct{}

func Register(mux *http.ServeMux, service API) huma.API {
	config := huma.DefaultConfig("GitOne API", "1.0.0")
	config.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"basicAuth": {
			Type:        "http",
			Scheme:      "basic",
			Description: "GitOne HTTP Basic authentication",
		},
		"cookieAuth": {
			Type:        "apiKey",
			In:          "cookie",
			Name:        auth.SessionCookieName,
			Description: "GitOne signed and encrypted browser session",
		},
		"runnerAuth": {
			Type:        "http",
			Scheme:      "bearer",
			Description: "GitOne remote runner token",
		},
	}
	api := humago.New(mux, config)

	huma.Register(api, huma.Operation{
		OperationID: "health",
		Method:      http.MethodGet,
		Path:        "/healthz",
		Summary:     "Check server health",
		Tags:        []string{"Health"},
	}, service.health)

	registerSessionAPI(api, service)
	registerRunnerAPI(api, service)
	if service.Coordinator != nil {
		mux.HandleFunc("GET /api/runner/source", service.runnerSource)
	}

	huma.Register(api, protected(huma.Operation{
		OperationID: "list-groups",
		Method:      http.MethodGet,
		Path:        "/api/groups",
		Summary:     "List accessible top-level groups",
		Tags:        []string{"Groups"},
	}), service.listGroups)

	huma.Register(api, protected(huma.Operation{
		OperationID: "get-group",
		Method:      http.MethodGet,
		Path:        "/api/groups/{path}",
		Summary:     "Get a group with its immediate children",
		Tags:        []string{"Groups"},
	}), service.getGroup)

	huma.Register(api, protected(huma.Operation{
		OperationID: "get-group-settings",
		Method:      http.MethodGet,
		Path:        "/api/groups/{path}/settings",
		Summary:     "Get complete group control settings",
		Tags:        []string{"Groups"},
	}), service.getGroupSettings)

	huma.Register(api, protected(huma.Operation{
		OperationID:   "create-group",
		Method:        http.MethodPost,
		Path:          "/api/groups/{path}",
		Summary:       "Create a group",
		Tags:          []string{"Groups"},
		DefaultStatus: http.StatusCreated,
	}), service.createGroup)

	huma.Register(api, protected(huma.Operation{
		OperationID:   "rename-group",
		Method:        http.MethodPatch,
		Path:          "/api/groups/{path}",
		Summary:       "Rename a group",
		Tags:          []string{"Groups"},
		DefaultStatus: http.StatusNoContent,
	}), service.renameGroup)

	huma.Register(api, protected(huma.Operation{
		OperationID: "update-group-settings",
		Method:      http.MethodPut,
		Path:        "/api/groups/{path}/settings",
		Summary:     "Update complete group control settings",
		Tags:        []string{"Groups"},
	}), service.updateGroupSettings)

	huma.Register(api, protected(huma.Operation{
		OperationID:   "delete-group",
		Method:        http.MethodDelete,
		Path:          "/api/groups/{path}",
		Summary:       "Delete an empty group",
		Tags:          []string{"Groups"},
		DefaultStatus: http.StatusNoContent,
	}), service.deleteGroup)

	huma.Register(api, protected(huma.Operation{
		OperationID:   "create-repository",
		Method:        http.MethodPost,
		Path:          "/api/repositories/{path}",
		Summary:       "Create a repository",
		Tags:          []string{"Repositories"},
		DefaultStatus: http.StatusCreated,
	}), service.createRepository)

	huma.Register(api, protected(huma.Operation{
		OperationID:   "import-repository",
		Method:        http.MethodPost,
		Path:          "/api/repositories/{path}/import",
		Summary:       "Import a bare repository from an HTTP or HTTPS remote",
		Tags:          []string{"Repositories"},
		DefaultStatus: http.StatusCreated,
	}), service.importRepository)

	huma.Register(api, protected(huma.Operation{
		OperationID:   "rename-repository",
		Method:        http.MethodPatch,
		Path:          "/api/repositories/{path}",
		Summary:       "Rename a repository",
		Tags:          []string{"Repositories"},
		DefaultStatus: http.StatusNoContent,
	}), service.renameRepository)

	huma.Register(api, protected(huma.Operation{
		OperationID:   "delete-repository",
		Method:        http.MethodDelete,
		Path:          "/api/repositories/{path}",
		Summary:       "Delete a repository",
		Tags:          []string{"Repositories"},
		DefaultStatus: http.StatusNoContent,
	}), service.deleteRepository)

	registerRepositoryBrowser(api, service)
	registerBuildAPI(api, service)
	registerReviewAPI(api, service)

	return api
}

func (a API) health(context.Context, *struct{}) (*healthOutput, error) {
	output := &healthOutput{}
	output.Body.Status = "ok"
	return output, nil
}

func (a API) listGroups(ctx context.Context, input *listGroupsInput) (*listGroupsOutput, error) {
	groups, listErr := a.Storage.ListGroups()
	if listErr != nil {
		return nil, huma.Error500InternalServerError("could not list groups", listErr)
	}

	output := &listGroupsOutput{}
	output.Body.Groups = []groupSummary{}
	identity, identityErr := a.authenticateIdentity(ctx, input.AuthInput)
	authenticated := identityErr == nil
	for _, group := range groups {
		var principal auth.Principal
		var resolveErr error
		if identityErr == nil {
			principal, resolveErr = a.Resolver.AuthorizeIdentity(ctx, group.Path, identity)
		} else {
			principal, resolveErr = a.authenticateBasicGroup(ctx, input.AuthInput, group.Path)
		}
		if resolveErr != nil {
			continue
		}
		authenticated = true
		if strings.Contains(group.Path, "/") || !principal.Role.Allows(control.RoleRead) {
			continue
		}
		description := ""
		if document, loadErr := a.Resolver.Controls.Load(ctx, group.Path); loadErr == nil {
			description = document.Description
		}
		output.Body.Groups = append(output.Body.Groups, groupSummary{
			Name:        group.Path,
			Path:        group.Path,
			Description: description,
			Role:        principal.Role,
		})
	}
	if !authenticated {
		return nil, huma.Error401Unauthorized("invalid credentials")
	}
	return output, nil
}

func (a API) getGroup(ctx context.Context, input *GroupPathInput) (*groupDetailOutput, error) {
	path, err := canonicalGroup(input.Path)
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	principal, err := a.authorizePrincipal(ctx, input.AuthInput, path, control.RoleRead)
	if err != nil {
		return nil, err
	}
	groups, listErr := a.Storage.ListGroups()
	if listErr != nil {
		return nil, huma.Error500InternalServerError("could not list groups", listErr)
	}

	var current *storage.GroupInfo
	output := &groupDetailOutput{}
	output.Body.Path = path
	output.Body.Username = principal.Name
	if document, loadErr := a.Resolver.Controls.Load(ctx, path); loadErr == nil {
		output.Body.Description = document.Description
	}
	output.Body.Subgroups = []groupSummary{}
	output.Body.Repositories = []repositorySummary{}
	prefix := path + "/"
	for i := range groups {
		group := &groups[i]
		if group.Path == path {
			current = group
			continue
		}
		if !strings.HasPrefix(group.Path, prefix) {
			continue
		}
		name := strings.TrimPrefix(group.Path, prefix)
		if strings.Contains(name, "/") {
			continue
		}
		subgroupPrincipal, authErr := a.authorizePrincipal(
			ctx,
			input.AuthInput,
			group.Path,
			control.RoleRead,
		)
		if authErr != nil || len(subgroupPrincipal.Repositories) > 0 {
			continue
		}
		description := ""
		if document, loadErr := a.Resolver.Controls.Load(ctx, group.Path); loadErr == nil {
			description = document.Description
		}
		output.Body.Subgroups = append(output.Body.Subgroups, groupSummary{
			Name:        name,
			Path:        group.Path,
			Description: description,
			Role:        subgroupPrincipal.Role,
		})
	}
	if current == nil {
		return nil, huma.Error404NotFound("group not found")
	}
	for _, name := range current.Repositories {
		if !principal.AllowsRepository(name) {
			continue
		}
		description, descriptionErr := a.Storage.RepositoryDescription(repopath.Repository{
			Groups: strings.Split(path, "/"),
			Name:   name,
		})
		if descriptionErr != nil {
			description = ""
		}
		output.Body.Repositories = append(output.Body.Repositories, repositorySummary{
			Name:        name,
			Description: description,
		})
	}
	return output, nil
}

func (a API) getGroupSettings(ctx context.Context, input *GroupPathInput) (*groupSettingsOutput, error) {
	path, err := canonicalGroup(input.Path)
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	if _, err = a.authorize(ctx, input.AuthInput, path, control.RoleAdmin); err != nil {
		return nil, err
	}
	document, err := a.Resolver.Controls.Load(ctx, path)
	if err != nil {
		return nil, huma.Error500InternalServerError("could not load group settings", err)
	}
	output := &groupSettingsOutput{}
	output.Body = document
	return output, nil
}

func (a API) updateGroupSettings(ctx context.Context, input *updateGroupSettingsInput) (*updateGroupSettingsOutput, error) {
	path, err := canonicalGroup(input.Path)
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	releaseOperation, err := a.acquireOperationLock()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = releaseOperation()
	}()
	a.Resolver.Controls.Invalidate(path)
	author, err := a.authorize(ctx, input.AuthInput, path, control.RoleAdmin)
	if err != nil {
		return nil, err
	}
	current, err := a.Resolver.Controls.Load(ctx, path)
	if err != nil {
		return nil, huma.Error500InternalServerError("could not load current group settings", err)
	}
	name := strings.TrimSpace(input.Body.Name)
	if name == "" || strings.Contains(name, "/") {
		return nil, huma.Error400BadRequest("group name must be one path segment")
	}
	parts := strings.Split(path, "/")
	parts[len(parts)-1] = name
	target, err := canonicalGroup(strings.Join(parts, "/"))
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	document := control.Document{
		Version:      1,
		Group:        target,
		Description:  input.Body.Description,
		Inherit:      input.Body.Inherit,
		Members:      input.Body.Members,
		Repositories: input.Body.Repositories,
	}
	if document.Members == nil {
		document.Members = map[string]control.Role{}
	}
	document.Tokens = make([]control.Token, 0, len(input.Body.Tokens))
	for _, submitted := range input.Body.Tokens {
		hash := ""
		if submitted.NewSecret != "" {
			hash, err = auth.HashSecret(submitted.NewSecret)
			if err != nil {
				return nil, huma.Error500InternalServerError("could not secure token secret", err)
			}
		} else {
			for _, existing := range current.Tokens {
				if existing.Key == submitted.Key && existing.Hash == submitted.Hash {
					hash = existing.Hash
					break
				}
			}
			if hash == "" {
				return nil, huma.Error400BadRequest(
					fmt.Sprintf("token %q needs a new secret", submitted.Name),
				)
			}
		}
		document.Tokens = append(document.Tokens, control.Token{
			Name:         submitted.Name,
			Key:          submitted.Key,
			Hash:         hash,
			Role:         submitted.Role,
			Repositories: submitted.Repositories,
			ExpiresAt:    submitted.ExpiresAt,
			Disabled:     submitted.Disabled,
		})
	}
	if document.Repositories == nil {
		document.Repositories = map[string]control.RepositoryPolicy{}
	}
	if err = control.Validate(target, document); err != nil {
		return nil, huma.Error400BadRequest("invalid group settings", err)
	}

	if target == path {
		if err = a.Storage.UpdateGroupControlLocked(path, document, author); err != nil {
			return nil, huma.Error500InternalServerError("could not update group settings", err)
		}
		a.Resolver.Controls.Invalidate(path)
	} else if err = a.renameGroupControlsLocked(ctx, path, target, document, author); err != nil {
		return nil, huma.Error409Conflict("could not rename group", err)
	}

	output := &updateGroupSettingsOutput{}
	output.Body.Path = target
	output.Body.Settings = document
	return output, nil
}

type groupControlRename struct {
	oldPath  string
	newPath  string
	original control.Document
	updated  control.Document
}

func (a API) renameGroupControls(
	ctx context.Context,
	path string,
	target string,
	current control.Document,
	author string,
) error {
	releaseOperation, err := a.acquireOperationLock()
	if err != nil {
		return err
	}
	defer func() {
		_ = releaseOperation()
	}()
	return a.renameGroupControlsLocked(ctx, path, target, current, author)
}

func (a API) renameGroupControlsLocked(
	ctx context.Context,
	path string,
	target string,
	current control.Document,
	author string,
) error {
	groups, err := a.Storage.ListGroups()
	if err != nil {
		return err
	}
	renames := make([]groupControlRename, 0)
	for _, group := range groups {
		if group.Path != path && !strings.HasPrefix(group.Path, path+"/") {
			continue
		}
		original, loadErr := a.Resolver.Controls.Load(ctx, group.Path)
		if loadErr != nil {
			return loadErr
		}
		newPath := target + strings.TrimPrefix(group.Path, path)
		updated := original
		updated.Group = newPath
		if group.Path == path {
			updated = current
		}
		if validateErr := control.Validate(newPath, updated); validateErr != nil {
			return validateErr
		}
		renames = append(renames, groupControlRename{
			oldPath:  group.Path,
			newPath:  newPath,
			original: original,
			updated:  updated,
		})
	}
	if len(renames) == 0 {
		return fmt.Errorf("group not found")
	}
	if err = a.Storage.RenameGroupLocked(path, target); err != nil {
		return err
	}
	for _, rename := range renames {
		if err = a.Storage.UpdateGroupControlLocked(rename.newPath, rename.updated, author); err != nil {
			rollbackErr := a.Storage.RenameGroupLocked(target, path)
			if rollbackErr == nil {
				for _, rollback := range renames {
					_ = a.Storage.UpdateGroupControlLocked(
						rollback.oldPath,
						rollback.original,
						author,
					)
				}
			}
			a.invalidateGroupRenames(renames)
			if rollbackErr != nil {
				return errors.Join(err, fmt.Errorf("rollback failed: %w", rollbackErr))
			}
			return err
		}
	}
	a.invalidateGroupRenames(renames)
	return nil
}

func (a API) invalidateGroupRenames(renames []groupControlRename) {
	for _, rename := range renames {
		a.Resolver.Controls.Invalidate(rename.oldPath)
		a.Resolver.Controls.Invalidate(rename.newPath)
	}
}

func (a API) createGroup(ctx context.Context, input *createGroupInput) (*createGroupOutput, error) {
	path, err := canonicalGroup(input.Path)
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	releaseOperation, err := a.acquireOperationLock()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = releaseOperation()
	}()
	a.Resolver.Controls.Invalidate(path)
	owner := ""
	if strings.Contains(path, "/") {
		owner, err = a.authorize(ctx, input.AuthInput, path, control.RoleAdmin)
		if err != nil {
			return nil, err
		}
	} else {
		identity, authErr := a.authenticateIdentity(ctx, input.AuthInput)
		if authErr != nil {
			return nil, authErr
		}
		owner = identity.Name
	}
	if err = a.Storage.CreateGroupLocked(path, owner, input.Description); err != nil {
		return nil, huma.Error409Conflict(err.Error())
	}
	a.Resolver.Controls.Invalidate(path)
	output := &createGroupOutput{}
	output.Body.Path = path
	return output, nil
}

func (a API) renameGroup(ctx context.Context, input *renameGroupInput) (*emptyOutput, error) {
	path, err := canonicalGroup(input.Path)
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	newPath, err := canonicalGroup(input.Body.NewPath)
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	releaseOperation, err := a.acquireOperationLock()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = releaseOperation()
	}()
	a.Resolver.Controls.Invalidate(path)
	author, err := a.authorize(ctx, input.AuthInput, path, control.RoleAdmin)
	if err != nil {
		return nil, err
	}
	document, err := a.Resolver.Controls.Load(ctx, path)
	if err != nil {
		return nil, huma.Error500InternalServerError("could not load group settings", err)
	}
	document.Group = newPath
	if err = a.renameGroupControlsLocked(ctx, path, newPath, document, author); err != nil {
		return nil, huma.Error409Conflict(err.Error())
	}
	return &emptyOutput{}, nil
}

func (a API) deleteGroup(ctx context.Context, input *GroupPathInput) (*emptyOutput, error) {
	path, err := canonicalGroup(input.Path)
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	releaseOperation, err := a.acquireOperationLock()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = releaseOperation()
	}()
	a.Resolver.Controls.Invalidate(path)
	if _, err = a.authorize(ctx, input.AuthInput, path, control.RoleAdmin); err != nil {
		return nil, err
	}
	if err = a.Storage.DeleteGroupLocked(path); err != nil {
		return nil, huma.Error409Conflict(err.Error())
	}
	a.Resolver.Controls.Invalidate(path)
	return &emptyOutput{}, nil
}

func (a API) createRepository(ctx context.Context, input *createRepositoryInput) (*createRepositoryOutput, error) {
	repository, err := parseRepositoryPath(input.Path)
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	releaseOperation, err := a.acquireOperationLock()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = releaseOperation()
	}()
	a.Resolver.Controls.Invalidate(repository.Group())
	principal, err := a.authorizeRepository(ctx, input.AuthInput, repository, control.RoleAdmin)
	if err != nil {
		return nil, err
	}
	if err = a.Storage.CreateRepositoryLocked(repository, storage.CreateRepositoryOptions{
		InitializeReadme: input.InitializeReadme,
		Author:           principal.Name,
		Description:      input.Description,
	}); err != nil {
		return nil, huma.Error409Conflict(err.Error())
	}
	output := &createRepositoryOutput{}
	output.Body.Group = repository.Group()
	output.Body.Name = repository.Name
	return output, nil
}

func (a API) importRepository(ctx context.Context, input *importRepositoryInput) (*createRepositoryOutput, error) {
	repository, err := parseRepositoryPath(input.Path)
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	remoteURL, err := canonicalImportURL(input.Body.URL)
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	username := strings.TrimSpace(input.Body.Username)
	if input.Body.Password != "" && username == "" {
		return nil, huma.Error400BadRequest(
			"an authentication username is required when a password or token is supplied",
		)
	}

	releaseOperation, err := a.acquireOperationLock()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = releaseOperation()
	}()
	a.Resolver.Controls.Invalidate(repository.Group())
	if _, err = a.authorizeRepository(
		ctx,
		input.AuthInput,
		repository,
		control.RoleAdmin,
	); err != nil {
		return nil, err
	}
	err = a.Storage.ImportRepositoryLocked(ctx, repository, storage.ImportRepositoryOptions{
		URL:      remoteURL,
		Username: username,
		Password: input.Body.Password,
	})
	if err != nil {
		var remoteError *storage.RemoteImportError
		if errors.As(err, &remoteError) {
			return nil, huma.Error502BadGateway(remoteError.Error())
		}
		return nil, huma.Error409Conflict(err.Error())
	}
	output := &createRepositoryOutput{}
	output.Body.Group = repository.Group()
	output.Body.Name = repository.Name
	return output, nil
}

func (a API) renameRepository(ctx context.Context, input *renameRepositoryInput) (*emptyOutput, error) {
	repository, err := parseRepositoryPath(input.Path)
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	releaseOperation, err := a.acquireOperationLock()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = releaseOperation()
	}()
	a.Resolver.Controls.Invalidate(repository.Group())
	if _, err = a.authorizeRepository(ctx, input.AuthInput, repository, control.RoleAdmin); err != nil {
		return nil, err
	}
	if err = a.Storage.RenameRepositoryLocked(
		repository,
		strings.TrimSuffix(input.Body.NewName, ".git"),
	); err != nil {
		return nil, huma.Error409Conflict(err.Error())
	}
	return &emptyOutput{}, nil
}

func (a API) deleteRepository(ctx context.Context, input *RepositoryPathInput) (*emptyOutput, error) {
	repository, err := parseRepositoryPath(input.Path)
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	releaseOperation, err := a.acquireOperationLock()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = releaseOperation()
	}()
	a.Resolver.Controls.Invalidate(repository.Group())
	if _, err = a.authorizeRepository(ctx, input.AuthInput, repository, control.RoleAdmin); err != nil {
		return nil, err
	}
	if err = a.Storage.DeleteRepositoryLocked(repository); err != nil {
		return nil, huma.Error409Conflict(err.Error())
	}
	return &emptyOutput{}, nil
}

func (a API) authorize(ctx context.Context, credentials AuthInput, group string, need control.Role) (string, error) {
	principal, err := a.authorizePrincipal(ctx, credentials, group, need)
	if err != nil {
		return "", err
	}
	if len(principal.Repositories) > 0 {
		return "", huma.Error403Forbidden("repository-scoped tokens cannot manage groups")
	}
	return principal.Name, nil
}

func (a API) authorizeRepository(
	ctx context.Context,
	credentials AuthInput,
	repository repopath.Repository,
	need control.Role,
) (auth.Principal, error) {
	principal, err := a.authorizePrincipal(ctx, credentials, repository.Group(), need)
	if err != nil {
		return auth.Principal{}, err
	}
	if !principal.AllowsRepository(repository.Name) {
		return auth.Principal{}, huma.Error403Forbidden("token cannot access this repository")
	}
	return principal, nil
}

func (a API) authorizePrincipal(
	ctx context.Context,
	credentials AuthInput,
	group string,
	need control.Role,
) (auth.Principal, error) {
	var principal auth.Principal
	var authErr error
	if identity, ok := a.sessionIdentity(credentials); ok {
		principal, authErr = a.Resolver.AuthorizeIdentity(ctx, group, identity)
		if authErr != nil {
			return auth.Principal{}, huma.Error403Forbidden("forbidden")
		}
	} else {
		principal, authErr = a.authenticateBasicGroup(ctx, credentials, group)
	}
	if authErr != nil {
		return auth.Principal{}, huma.Error401Unauthorized("invalid credentials")
	}
	if !principal.Role.Allows(need) {
		return auth.Principal{}, huma.Error403Forbidden("forbidden")
	}
	return principal, nil
}

func (a API) authenticateIdentity(ctx context.Context, credentials AuthInput) (auth.Principal, error) {
	if identity, ok := a.sessionIdentity(credentials); ok {
		return identity, nil
	}
	user, secret, err := basicCredentials(credentials.Authorization)
	if err != nil {
		return auth.Principal{}, err
	}
	principal, authErr := a.Resolver.AuthenticateIdentity(ctx, user, secret)
	if authErr != nil {
		return auth.Principal{}, huma.Error401Unauthorized("invalid credentials")
	}
	return principal, nil
}

func (a API) authenticateBasicGroup(
	ctx context.Context,
	credentials AuthInput,
	group string,
) (auth.Principal, error) {
	user, secret, err := basicCredentials(credentials.Authorization)
	if err != nil {
		return auth.Principal{}, err
	}
	return a.Resolver.Authenticate(ctx, group, user, secret)
}

func (a API) sessionIdentity(credentials AuthInput) (auth.Principal, bool) {
	if a.Sessions == nil {
		return auth.Principal{}, false
	}
	username, err := a.Sessions.Username(credentials.Cookie)
	if err != nil {
		return auth.Principal{}, false
	}
	return auth.Principal{Name: username}, true
}

func (a API) credentialUsername(credentials AuthInput) (string, error) {
	if identity, ok := a.sessionIdentity(credentials); ok {
		return identity.Name, nil
	}
	user, _, err := basicCredentials(credentials.Authorization)
	return user, err
}

func basicCredentials(header string) (string, string, error) {
	request := &http.Request{Header: http.Header{"Authorization": []string{header}}}
	user, secret, ok := request.BasicAuth()
	if !ok {
		return "", "", huma.Error401Unauthorized("authentication required")
	}
	return user, secret, nil
}

func canonicalGroup(value string) (string, error) {
	parts, err := repopath.ParseGroup(value)
	if err != nil {
		return "", err
	}
	return strings.Join(parts, "/"), nil
}

func parseRepositoryPath(value string) (repopath.Repository, error) {
	repository, _, err := repopath.ParseGitRequestPath("/" + strings.TrimSuffix(value, ".git") + ".git/info/refs")
	return repository, err
}

func canonicalImportURL(value string) (string, error) {
	remote, err := url.ParseRequestURI(strings.TrimSpace(value))
	if err != nil || remote.Hostname() == "" {
		return "", errors.New("a valid absolute HTTP or HTTPS remote URL is required")
	}
	remote.Scheme = strings.ToLower(remote.Scheme)
	if remote.Scheme != "http" && remote.Scheme != "https" {
		return "", errors.New("remote URL scheme must be HTTP or HTTPS")
	}
	if remote.User != nil {
		return "", errors.New(
			"remote URL must not contain credentials; use the username and password fields",
		)
	}
	if remote.Fragment != "" {
		return "", errors.New("remote URL must not contain a fragment")
	}
	return remote.String(), nil
}

func protected(operation huma.Operation) huma.Operation {
	operation.Security = []map[string][]string{
		{"basicAuth": []string{}},
		{"cookieAuth": []string{}},
	}
	return operation
}
