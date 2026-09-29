// Package api embeds the HTTP contract (D-20): api/openapi.yaml is the single
// source of the contract, served at /openapi.yaml and used by the tests.
package api

import _ "embed" // go:embed

// OpenAPI is the OpenAPI 3.0.3 document of the API.
//
//go:embed openapi.yaml
var OpenAPI []byte

// SwaggerHTML is the page served at /docs.
//
//go:embed swagger.html
var SwaggerHTML []byte
