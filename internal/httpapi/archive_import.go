package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"

	"github.com/danielgtaylor/huma/v2"
	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/storage"
)

type archiveImportProblem struct {
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
}

func registerRepositoryArchiveImport(mux *http.ServeMux, api huma.API, service API) {
	mux.HandleFunc(
		"POST /api/repositories/{path}/import-archive",
		service.importRepositoryArchiveHTTP,
	)

	binaryArchive := &huma.MediaType{
		Schema: &huma.Schema{Type: huma.TypeString, Format: "binary"},
	}
	operation := protected(huma.Operation{
		OperationID:   "import-repository-archive",
		Method:        http.MethodPost,
		Path:          "/api/repositories/{path}/import-archive",
		Summary:       "Import a bare repository from a ZIP or TAR archive",
		Description:   "The request body is the archive file. The archive may contain the bare Git repository at its root or in one enclosing folder.",
		Tags:          []string{"Repositories"},
		DefaultStatus: http.StatusCreated,
		Errors: []int{
			http.StatusBadRequest,
			http.StatusUnauthorized,
			http.StatusForbidden,
			http.StatusConflict,
			http.StatusRequestEntityTooLarge,
			http.StatusInternalServerError,
		},
		Parameters: []*huma.Param{
			{
				Name:        "path",
				In:          "path",
				Description: "URL-encoded group and repository path",
				Required:    true,
				Schema:      &huma.Schema{Type: huma.TypeString},
			},
			{
				Name:        "filename",
				In:          "query",
				Description: "Original filename ending in .zip, .tar, .tar.gz, or .tgz",
				Required:    true,
				Schema:      &huma.Schema{Type: huma.TypeString},
			},
		},
		RequestBody: &huma.RequestBody{
			Description: "ZIP or TAR archive containing one bare Git repository",
			Required:    true,
			Content: map[string]*huma.MediaType{
				"application/octet-stream": binaryArchive,
				"application/zip":          binaryArchive,
				"application/x-tar":        binaryArchive,
				"application/gzip":         binaryArchive,
			},
		},
		Responses: map[string]*huma.Response{
			"201": {Description: "Repository imported"},
		},
	})
	api.OpenAPI().AddOperation(&operation)
}

func (a API) importRepositoryArchiveHTTP(w http.ResponseWriter, request *http.Request) {
	repository, err := parseRepositoryPath(request.PathValue("path"))
	if err != nil {
		writeArchiveImportProblem(w, http.StatusBadRequest, err.Error())
		return
	}
	filename := request.URL.Query().Get("filename")
	if !storage.IsSupportedImportArchive(filename) {
		writeArchiveImportProblem(
			w,
			http.StatusBadRequest,
			"filename must end in .zip, .tar, .tar.gz, or .tgz",
		)
		return
	}
	credentials := AuthInput{
		Authorization: request.Header.Get("Authorization"),
		Cookie:        request.Header.Get("Cookie"),
	}
	a.Resolver.Controls.Invalidate(repository.Group())
	if _, err = a.authorizeRepository(
		request.Context(),
		credentials,
		repository,
		control.RoleAdmin,
	); err != nil {
		writeArchiveImportAPIError(w, err)
		return
	}
	if request.ContentLength > storage.MaximumImportArchiveBytes {
		writeArchiveImportProblem(
			w,
			http.StatusRequestEntityTooLarge,
			"archive upload exceeds the 1 GiB limit",
		)
		return
	}

	upload, err := os.CreateTemp("", "gitone-import-upload-*")
	if err != nil {
		writeArchiveImportProblem(
			w,
			http.StatusInternalServerError,
			"could not prepare the archive upload",
		)
		return
	}
	uploadPath := upload.Name()
	defer func() {
		_ = os.Remove(uploadPath)
	}()

	written, copyErr := io.Copy(
		upload,
		io.LimitReader(request.Body, storage.MaximumImportArchiveBytes+1),
	)
	closeErr := upload.Close()
	if copyErr != nil {
		writeArchiveImportProblem(
			w,
			http.StatusBadRequest,
			"could not read the archive upload",
		)
		return
	}
	if closeErr != nil {
		writeArchiveImportProblem(
			w,
			http.StatusInternalServerError,
			"could not store the archive upload",
		)
		return
	}
	if written > storage.MaximumImportArchiveBytes {
		writeArchiveImportProblem(
			w,
			http.StatusRequestEntityTooLarge,
			"archive upload exceeds the 1 GiB limit",
		)
		return
	}
	if written == 0 {
		writeArchiveImportProblem(w, http.StatusBadRequest, "archive upload is empty")
		return
	}

	err = a.Storage.ImportRepositoryArchiveValidated(
		request.Context(),
		repository,
		filename,
		uploadPath,
		func() error {
			a.Resolver.Controls.Invalidate(repository.Group())
			_, authorizeErr := a.authorizeRepository(
				request.Context(),
				credentials,
				repository,
				control.RoleAdmin,
			)
			return authorizeErr
		},
	)
	if err != nil {
		var archiveError *storage.ArchiveImportError
		if errors.As(err, &archiveError) {
			writeArchiveImportProblem(w, http.StatusBadRequest, archiveError.Error())
			return
		}
		writeArchiveImportProblem(w, http.StatusConflict, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"group": repository.Group(),
		"name":  repository.Name,
	})
}

func writeArchiveImportAPIError(w http.ResponseWriter, err error) {
	var statusError huma.StatusError
	if errors.As(err, &statusError) {
		writeArchiveImportProblem(w, statusError.GetStatus(), statusError.Error())
		return
	}
	writeArchiveImportProblem(
		w,
		http.StatusInternalServerError,
		"could not import the repository archive",
	)
}

func writeArchiveImportProblem(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(archiveImportProblem{
		Title:  http.StatusText(status),
		Status: status,
		Detail: detail,
	})
}
