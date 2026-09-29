package config

import "go.uber.org/fx"

// Module provides the validated Config; a load error aborts the application (FX-02).
var Module = fx.Module("config", fx.Provide(Load))
