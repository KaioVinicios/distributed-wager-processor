package httpapi

import (
	"net/http"

	"github.com/KaioVinicios/pda/api"
)

// openAPI serves the embedded contract: always the version of the build.
func openAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(api.OpenAPI) // nothing useful to do if the client went away
}

// swaggerUI serves the Swagger UI page (D-20).
func swaggerUI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(api.SwaggerHTML) // nothing useful to do if the client went away
}
