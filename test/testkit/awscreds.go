package testkit

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// AWSKeys is one profile of the shared credentials file.
type AWSKeys struct {
	AccessKeyID     string
	SecretAccessKey string
}

// ParseAWSCredentials parses an AWS shared credentials (INI) file.
func ParseAWSCredentials(r io.Reader) (map[string]AWSKeys, error) {
	out := map[string]AWSKeys{}
	var profile string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			profile = strings.TrimSpace(line[1 : len(line)-1])
		default:
			k, v, ok := strings.Cut(line, "=")
			if !ok || profile == "" {
				continue
			}
			keys := out[profile]
			switch strings.TrimSpace(k) {
			case "aws_access_key_id":
				keys.AccessKeyID = strings.TrimSpace(v)
			case "aws_secret_access_key":
				keys.SecretAccessKey = strings.TrimSpace(v)
			}
			out[profile] = keys
		}
	}
	return out, sc.Err()
}

// AWSProfiles reads .local/aws/credentials written by aws-init.
func AWSProfiles(tb testing.TB) map[string]AWSKeys {
	tb.Helper()
	f, err := os.Open(filepath.Join(RepoRoot(tb), ".local", "aws", "credentials"))
	if err != nil {
		tb.Fatalf("open credentials (run make infra-up): %v", err)
	}
	defer f.Close()
	profiles, err := ParseAWSCredentials(f)
	if err != nil {
		tb.Fatalf("parse credentials: %v", err)
	}
	return profiles
}
