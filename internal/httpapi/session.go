package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

type loginBody struct {
	Username string `json:"username" minLength:"1"`
	Password string `json:"password" minLength:"1"`
}

type loginInput struct {
	Body loginBody
}

type sessionInput struct {
	Cookie string `header:"Cookie" hidden:"true"`
}

type sessionOutput struct {
	Body struct {
		Username string `json:"username"`
	}
}

type loginOutput struct {
	SetCookie string `header:"Set-Cookie"`
	Body      struct {
		Username string `json:"username"`
	}
}

type logoutOutput struct {
	SetCookie string `header:"Set-Cookie"`
}

func registerSessionAPI(api huma.API, service API) {
	huma.Register(api, huma.Operation{
		OperationID: "login",
		Method:      http.MethodPost,
		Path:        "/api/session",
		Summary:     "Authenticate with LDAP and create a browser session",
		Tags:        []string{"Session"},
	}, service.login)

	huma.Register(api, huma.Operation{
		OperationID: "get-session",
		Method:      http.MethodGet,
		Path:        "/api/session",
		Summary:     "Read the current browser session",
		Tags:        []string{"Session"},
		Security: []map[string][]string{
			{"cookieAuth": []string{}},
		},
	}, service.getSession)

	huma.Register(api, huma.Operation{
		OperationID:   "logout",
		Method:        http.MethodDelete,
		Path:          "/api/session",
		Summary:       "Clear the current browser session",
		Tags:          []string{"Session"},
		DefaultStatus: http.StatusNoContent,
	}, service.logout)
}

func (a API) login(ctx context.Context, input *loginInput) (*loginOutput, error) {
	if a.Sessions == nil {
		return nil, huma.Error500InternalServerError("browser sessions are not configured")
	}
	username := strings.TrimSpace(input.Body.Username)
	principal, err := a.Resolver.AuthenticateIdentity(ctx, username, input.Body.Password)
	if err != nil {
		return nil, huma.Error401Unauthorized("invalid username or password")
	}
	cookie, err := a.Sessions.CookieHeader(principal.Name)
	if err != nil {
		return nil, huma.Error500InternalServerError("could not create browser session", err)
	}
	output := &loginOutput{SetCookie: cookie}
	output.Body.Username = principal.Name
	return output, nil
}

func (a API) getSession(_ context.Context, input *sessionInput) (*sessionOutput, error) {
	if a.Sessions == nil {
		return nil, huma.Error500InternalServerError("browser sessions are not configured")
	}
	username, err := a.Sessions.Username(input.Cookie)
	if err != nil {
		return nil, huma.Error401Unauthorized("login required")
	}
	output := &sessionOutput{}
	output.Body.Username = username
	return output, nil
}

func (a API) logout(context.Context, *struct{}) (*logoutOutput, error) {
	if a.Sessions == nil {
		return nil, huma.Error500InternalServerError("browser sessions are not configured")
	}
	return &logoutOutput{SetCookie: a.Sessions.ClearCookieHeader()}, nil
}
