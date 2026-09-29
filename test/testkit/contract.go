package testkit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"

	"github.com/KaioVinicios/pda/api"
)

// Contract validates requests and responses against api/openapi.yaml (D-20):
// a response off the contract fails the test that received it.
type Contract struct {
	router routers.Router
}

var registerDecoders sync.Once

// LoadContract loads the embedded document. The servers are dropped, so the
// routes match the application under test on any address.
func LoadContract() (*Contract, error) {
	registerDecoders.Do(func() {
		// The yaml decoder of kin-openapi would parse the document served at
		// /openapi.yaml into an object, and text/html has none: both bodies are
		// plain strings in the contract.
		plain := func(body io.Reader, _ http.Header, _ *openapi3.SchemaRef, _ openapi3filter.EncodingFn) (any, error) {
			b, err := io.ReadAll(body)
			return string(b), err
		}
		openapi3filter.RegisterBodyDecoder("application/yaml", plain)
		openapi3filter.RegisterBodyDecoder("text/html", plain)
	})
	doc, err := openapi3.NewLoader().LoadFromData(api.OpenAPI)
	if err != nil {
		return nil, fmt.Errorf("testkit: load the contract: %w", err)
	}
	doc.Servers = nil
	router, err := legacy.NewRouter(doc)
	if err != nil {
		return nil, fmt.Errorf("testkit: contract router: %w", err)
	}
	return &Contract{router: router}, nil
}

// Check validates one exchange: the response always, the request unless
// skipRequest (a request deliberately off the contract, spec decision 20). A
// route the document does not have (404/405 of routing) is not validated; I15
// guarantees the document and the routes are the same set.
func (c *Contract) Check(req *http.Request, reqBody []byte, resp *http.Response, respBody []byte, skipRequest bool) error {
	route, params, err := c.router.FindRoute(req)
	var notRouted *routers.RouteError // path or method outside the document
	if errors.As(err, &notRouted) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("contract: %w", err)
	}
	req.Body = io.NopCloser(bytes.NewReader(reqBody))
	ctx := context.WithoutCancel(req.Context())
	in := &openapi3filter.RequestValidationInput{
		Request: req, PathParams: params, Route: route,
		Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
	}
	if !skipRequest {
		if err := openapi3filter.ValidateRequest(ctx, in); err != nil {
			return fmt.Errorf("contract: request %s %s: %s", req.Method, req.URL.Path, firstLine(err))
		}
	}
	out := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: in, Status: resp.StatusCode, Header: resp.Header,
		Body:    io.NopCloser(bytes.NewReader(respBody)),
		Options: &openapi3filter.Options{IncludeResponseStatus: true},
	}
	if err := openapi3filter.ValidateResponse(ctx, out); err != nil {
		return fmt.Errorf("contract: response %d to %s %s: %s", resp.StatusCode, req.Method, req.URL.Path, firstLine(err))
	}
	return nil
}

// firstLine drops the schema dump kin-openapi appends to its errors.
func firstLine(err error) string {
	line, _, _ := strings.Cut(err.Error(), "\n")
	return line
}
