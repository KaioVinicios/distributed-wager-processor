package httpapi

import "go.uber.org/fx"

// Module provides the router and starts the API server.
var Module = fx.Module("httpapi", fx.Provide(NewMux), fx.Invoke(RegisterServer))
