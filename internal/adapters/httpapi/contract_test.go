package httpapi_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/KaioVinicios/pda/api"
	"github.com/KaioVinicios/pda/internal/adapters/httpapi"
)

// Covers: HTTP-01..09, DOC-06, D-20 (I15)
//
// The document is valid, examples included, and it describes exactly the
// routes the handler registers: a route added on one side only fails here.
func TestOpenAPIContract(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromData(api.OpenAPI)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := doc.Validate(t.Context(), openapi3.EnableExamplesValidation()); err != nil {
		t.Fatalf("validate: %v", err)
	}
	var documented []string
	for path, item := range doc.Paths.Map() {
		for method := range item.Operations() {
			documented = append(documented, strings.ToUpper(method)+" "+path)
		}
	}
	registered := httpapi.Routes(true)
	slices.Sort(documented)
	slices.Sort(registered)
	if !slices.Equal(documented, registered) {
		t.Fatalf("routes differ:\n documented %v\n registered %v", documented, registered)
	}
}
