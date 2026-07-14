package s3store

import (
	"context"
	"net"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type ClientConfig struct {
	Region                   string
	Endpoint                 string
	UsePathStyle             bool
	AllowInsecureDevelopment bool
}

func NewClient(ctx context.Context, configuration ClientConfig, options ...func(*awsconfig.LoadOptions) error) (*s3.Client, error) {
	if configuration.Region == "" {
		return nil, ErrConfiguration
	}
	if configuration.Endpoint != "" {
		endpoint, err := url.Parse(configuration.Endpoint)
		if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
			return nil, ErrConfiguration
		}
		if configuration.AllowInsecureDevelopment {
			if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
				return nil, ErrConfiguration
			}
			if endpoint.Scheme == "http" && !isLoopback(endpoint.Hostname()) {
				return nil, ErrConfiguration
			}
		} else if endpoint.Scheme != "https" {
			return nil, ErrConfiguration
		}
	}
	loadOptions := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(configuration.Region)}
	loadOptions = append(loadOptions, options...)
	config, err := awsconfig.LoadDefaultConfig(ctx, loadOptions...)
	if err != nil {
		return nil, err
	}
	return s3.NewFromConfig(config, func(s3Options *s3.Options) {
		if configuration.Endpoint != "" {
			s3Options.BaseEndpoint = aws.String(strings.TrimSuffix(configuration.Endpoint, "/"))
		}
		s3Options.UsePathStyle = configuration.UsePathStyle
	}), nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
