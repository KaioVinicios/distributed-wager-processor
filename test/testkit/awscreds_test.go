package testkit_test

import (
	"strings"
	"testing"

	"github.com/KaioVinicios/pda/test/testkit"
)

func TestParseAWSCredentials(t *testing.T) {
	in := "[pda-wallet-service]\naws_access_key_id = AKIA1\naws_secret_access_key = s1\n\n[provider-a]\naws_access_key_id=AKIA2\naws_secret_access_key=s2\n"
	got, err := testkit.ParseAWSCredentials(strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseAWSCredentials() error = %v", err)
	}
	if got["pda-wallet-service"] != (testkit.AWSKeys{AccessKeyID: "AKIA1", SecretAccessKey: "s1"}) ||
		got["provider-a"] != (testkit.AWSKeys{AccessKeyID: "AKIA2", SecretAccessKey: "s2"}) || len(got) != 2 {
		t.Fatalf("ParseAWSCredentials() = %+v", got)
	}
}
