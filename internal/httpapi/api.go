package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/define42/GitOne/internal/auth"
	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/storage"
)

type API struct {
	Storage  storage.Store
	Resolver *auth.Resolver
}

type AuthInput struct {
	Authorization string `header:"Authorization" hidden:"true"`
}

type healthOutput struct {
	Body struct {
		Status string `json:"status" example:"ok"`
	}
}

type groupSummary struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Description string `json:"description"`
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
	Tokens       []control.Token                     `json:"tokens"`
	Repositories map[string]control.RepositoryPolicy `json:"repositories"`
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
	Description      string `query:"description" doc:"Repository description stored in .gitone.json"`
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
	}
	api := humago.New(mux, config)

	huma.Register(api, huma.Operation{
		OperationID: "health",
		Method:      http.MethodGet,
		Path:        "/healthz",
		Summary:     "Check server health",
		Tags:        []string{"Health"},
	}, service.health)

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

	return api
}

func (a API) health(context.Context, *struct{}) (*healthOutput, error) {
	output := &healthOutput{}
	output.Body.Status = "ok"
	return output, nil
}

func (a API) listGroups(ctx context.Context, input *listGroupsInput) (*listGroupsOutput, error) {
	user, secret, err := basicCredentials(input.Authorization)
	if err != nil {
		return nil, err
	}
	groups, listErr := a.Storage.ListGroups()
	if listErr != nil {
		return nil, huma.Error500InternalServerError("could not list groups", listErr)
	}

	output := &listGroupsOutput{}
	output.Body.Groups = []groupSummary{}
	authenticated := false
	if _, resolveErr := a.Resolver.Authenticate(ctx, "", user, secret); resolveErr == nil {
		authenticated = true
	}
	for _, group := range groups {
		principal, resolveErr := a.Resolver.Authenticate(ctx, group.Path, user, secret)
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
	username, err := a.authorize(ctx, input.Authorization, path, control.RoleRead)
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
	output.Body.Username = username
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
		if _, authErr := a.authorize(ctx, input.Authorization, group.Path, control.RoleRead); authErr != nil {
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
		})
	}
	if current == nil {
		return nil, huma.Error404NotFound("group not found")
	}
	for _, name := range current.Repositories {
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
	if _, err = a.authorize(ctx, input.Authorization, path, control.RoleAdmin); err != nil {
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
	author, err := a.authorize(ctx, input.Authorization, path, control.RoleAdmin)
	if err != nil {
		return nil, err
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
		Tokens:       input.Body.Tokens,
		Repositories: input.Body.Repositories,
	}
	if document.Members == nil {
		document.Members = map[string]control.Role{}
	}
	if document.Tokens == nil {
		document.Tokens = []control.Token{}
	}
	if document.Repositories == nil {
		document.Repositories = map[string]control.RepositoryPolicy{}
	}
	if err = control.Validate(target, document); err != nil {
		return nil, huma.Error400BadRequest("invalid group settings", err)
	}

	if target == path {
		if err = a.Storage.UpdateGroupControl(path, document, author); err != nil {
			return nil, huma.Error500InternalServerError("could not update group settings", err)
		}
		a.Resolver.Controls.Invalidate(path)
	} else if err = a.renameGroupControls(ctx, path, target, document, author); err != nil {
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
	if err = a.Storage.RenameGroup(path, target); err != nil {
		return err
	}
	for _, rename := range renames {
		if err = a.Storage.UpdateGroupControl(rename.newPath, rename.updated, author); err != nil {
			rollbackErr := a.Storage.RenameGroup(target, path)
			if rollbackErr == nil {
				for _, rollback := range renames {
					_ = a.Storage.UpdateGroupControl(rollback.oldPath, rollback.original, author)
				}
			}
			a.invalidateGroupRenames(renames)
			if rollbackErr != nil {
				return fmt.Errorf("%w; rollback failed: %v", err, rollbackErr)
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
	owner, err := a.authorize(ctx, input.Authorization, path, control.RoleAdmin)
	if err != nil {
		return nil, err
	}
	if err = a.Storage.CreateGroup(path, owner, input.Description); err != nil {
		return nil, huma.Error409Conflict(err.Error())
	}
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
	author, err := a.authorize(ctx, input.Authorization, path, control.RoleAdmin)
	if err != nil {
		return nil, err
	}
	document, err := a.Resolver.Controls.Load(ctx, path)
	if err != nil {
		return nil, huma.Error500InternalServerError("could not load group settings", err)
	}
	document.Group = newPath
	if err = a.renameGroupControls(ctx, path, newPath, document, author); err != nil {
		return nil, huma.Error409Conflict(err.Error())
	}
	return &emptyOutput{}, nil
}

func (a API) deleteGroup(ctx context.Context, input *GroupPathInput) (*emptyOutput, error) {
	path, err := canonicalGroup(input.Path)
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	if _, err = a.authorize(ctx, input.Authorization, path, control.RoleAdmin); err != nil {
		return nil, err
	}
	if err = a.Storage.DeleteGroup(path); err != nil {
		return nil, huma.Error409Conflict(err.Error())
	}
	return &emptyOutput{}, nil
}

func (a API) createRepository(ctx context.Context, input *createRepositoryInput) (*createRepositoryOutput, error) {
	repository, err := parseRepositoryPath(input.Path)
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	author, err := a.authorize(ctx, input.Authorization, repository.Group(), control.RoleAdmin)
	if err != nil {
		return nil, err
	}
	if err = a.Storage.CreateRepository(repository, storage.CreateRepositoryOptions{
		InitializeReadme: input.InitializeReadme,
		Author:           author,
		Description:      input.Description,
	}); err != nil {
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
	if _, err = a.authorize(ctx, input.Authorization, repository.Group(), control.RoleAdmin); err != nil {
		return nil, err
	}
	if err = a.Storage.RenameRepository(repository, strings.TrimSuffix(input.Body.NewName, ".git")); err != nil {
		return nil, huma.Error409Conflict(err.Error())
	}
	return &emptyOutput{}, nil
}

func (a API) deleteRepository(ctx context.Context, input *RepositoryPathInput) (*emptyOutput, error) {
	repository, err := parseRepositoryPath(input.Path)
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	if _, err = a.authorize(ctx, input.Authorization, repository.Group(), control.RoleAdmin); err != nil {
		return nil, err
	}
	if err = a.Storage.DeleteRepository(repository); err != nil {
		return nil, huma.Error409Conflict(err.Error())
	}
	return &emptyOutput{}, nil
}

func (a API) authorize(ctx context.Context, header, group string, need control.Role) (string, error) {
	user, secret, err := basicCredentials(header)
	if err != nil {
		return "", err
	}
	principal, authErr := a.Resolver.Authenticate(ctx, group, user, secret)
	if authErr != nil {
		return "", huma.Error401Unauthorized("invalid credentials")
	}
	if !principal.Role.Allows(need) {
		return "", huma.Error403Forbidden("forbidden")
	}
	return principal.Name, nil
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

func protected(operation huma.Operation) huma.Operation {
	operation.Security = []map[string][]string{
		{"basicAuth": []string{}},
	}
	operation.Middlewares = append(operation.Middlewares, func(ctx huma.Context, next func(huma.Context)) {
		ctx.SetHeader("WWW-Authenticate", `Basic realm="GitOne"`)
		next(ctx)
	})
	return operation
}
