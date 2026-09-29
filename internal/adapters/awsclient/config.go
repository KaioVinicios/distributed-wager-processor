package awsclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"go.uber.org/fx"
)

const loadTimeout = 5 * time.Second

// NewAWSConfig loads the SDK configuration from the default chain
// (AWS_REGION, AWS_ENDPOINT_URL, env keys or AWS_SHARED_CREDENTIALS_FILE + AWS_PROFILE).
// A nil hc keeps the SDK default HTTP client. Fx constructors take no context,
// so a bounded one is created here.
func NewAWSConfig(hc *http.Client) (aws.Config, error) {
	ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
	defer cancel()
	var opts []func(*awsconfig.LoadOptions) error
	if hc != nil {
		opts = append(opts, awsconfig.WithHTTPClient(hc))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return aws.Config{}, fmt.Errorf("awsclient: load config: %w", err)
	}
	if cfg.Region == "" {
		return aws.Config{}, errors.New("awsclient: AWS_REGION is not set")
	}
	return cfg, nil
}

// newHTTPClient builds the SDK HTTP client from the SDK transport defaults and
// closes its idle keep-alive connections on stop, so the AWS clients shut down
// after everything that uses them (D-15).
func newHTTPClient(lc fx.Lifecycle) *http.Client {
	tr := awshttp.NewBuildableClient().GetTransport()
	lc.Append(fx.Hook{OnStop: func(context.Context) error {
		tr.CloseIdleConnections()
		return nil
	}})
	return &http.Client{Transport: tr}
}
