package services

import (
	"context"
	"expo-open-ota/config"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"log"
	"sync"
)

var (
	s3Client     *s3.Client
	s3ClientErr  error
	initS3Client sync.Once
)

func GetS3Client() (*s3.Client, error) {
	initS3Client.Do(func() {
		var cfg aws.Config
		opts := []func(*awsconfig.LoadOptions) error{
			awsconfig.WithRegion(config.GetEnv("AWS_REGION")),
		}
		accessKey := config.GetEnv("AWS_ACCESS_KEY_ID")
		secretKey := config.GetEnv("AWS_SECRET_ACCESS_KEY")
		if accessKey != "" && secretKey != "" {
			opts = append(opts, awsconfig.WithCredentialsProvider(
				aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
					return aws.Credentials{
						AccessKeyID:     accessKey,
						SecretAccessKey: secretKey,
					}, nil
				}),
			))
		}

		cfg, s3ClientErr = awsconfig.LoadDefaultConfig(context.TODO(), opts...)
		if s3ClientErr != nil {
			s3ClientErr = fmt.Errorf("error loading AWS configuration: %w", s3ClientErr)
			return
		}
		baseEndpoint := config.GetEnv("AWS_BASE_ENDPOINT")
		s3Client = s3.NewFromConfig(cfg, func(o *s3.Options) {
			// aws-sdk-go-v2 (Jan 2025+) defaults RequestChecksumCalculation to
			// WhenSupported, which bakes x-amz-sdk-checksum-algorithm /
			// x-amz-checksum-crc32 into (pre)signed PutObject requests. eoas PUTs
			// the raw file to the presigned URL without computing that checksum, so
			// Cloudflare R2 rejects the upload with "500 InternalError". Revert to
			// WhenRequired so presigned URLs don't demand a checksum the client
			// can't supply; relax response validation too, which also silences the
			// "Response has no supported checksum" warning on every download.
			o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
			o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
			if baseEndpoint != "" {
				o.BaseEndpoint = aws.String(baseEndpoint)
			}
		})
	})

	return s3Client, s3ClientErr
}

func FetchSecret(secretName string) string {
	cfg, err := awsconfig.LoadDefaultConfig(context.TODO())
	if err != nil {
		log.Fatalf("Failed to load AWS configuration: %v", err)
	}

	client := secretsmanager.NewFromConfig(cfg)

	resp, err := client.GetSecretValue(context.TODO(), &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(secretName),
	})
	if err != nil {
		log.Fatalf("Failed to retrieve secret %s: %v", secretName, err)
	}

	if resp.SecretString == nil {
		log.Fatalf("Secret %s has no SecretString", secretName)
	}

	return *resp.SecretString
}
