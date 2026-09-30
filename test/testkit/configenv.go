package testkit

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// EnvOf turns a struct with env tags (config.Config, config.Roles) into the
// environment variables that make a process read the same values back (spec
// M8, decision 6). Fields without an env tag are skipped.
func EnvOf(v any) map[string]string {
	rv := reflect.ValueOf(v)
	rt := rv.Type()
	out := make(map[string]string, rt.NumField())
	for i := range rt.NumField() {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("env"), ",")
		if name == "" {
			continue
		}
		out[name] = envValue(rv.Field(i).Interface())
	}
	return out
}

// envValue writes one field as the process parses it. A new field type fails
// TestEnvOf here instead of being written wrong.
func envValue(v any) string {
	switch v := v.(type) {
	case time.Duration:
		return v.String()
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case int:
		return strconv.Itoa(v)
	case int32:
		return strconv.FormatInt(int64(v), 10)
	default:
		panic(fmt.Sprintf("testkit: EnvOf cannot write a %T", v))
	}
}
