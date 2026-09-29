package awsclient_test

import (
	"os"
	"testing"

	"github.com/KaioVinicios/pda/internal/adapters/awsclient"
)

// Covers: FX-02
func TestNewAWSConfig_RequiresRegion(t *testing.T) {
	for _, k := range []string{"AWS_REGION", "AWS_DEFAULT_REGION", "AWS_PROFILE", "AWS_CONFIG_FILE"} {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatalf("unset %s: %v", k, err)
		}
	}
	t.Setenv("AWS_CONFIG_FILE", os.DevNull) // ignore the developer's ~/.aws/config

	if _, err := awsclient.NewAWSConfig(nil); err == nil {
		t.Fatal("NewAWSConfig() error = nil, want missing-region error")
	}
}

// Covers: FX-02
func TestNewAWSConfig_ReadsRegionFromEnvironment(t *testing.T) {
	t.Setenv("AWS_REGION", "us-east-1")
	cfg, err := awsclient.NewAWSConfig(nil)
	if err != nil || cfg.Region != "us-east-1" {
		t.Fatalf("NewAWSConfig() = %q, %v; want us-east-1", cfg.Region, err)
	}
}
