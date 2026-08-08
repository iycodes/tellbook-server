package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
	"time"

	"booking/go-server/internal/config"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

type R2Service struct {
	client              *s3.Client
	presignClient       *s3.PresignClient
	privateBucketName   string
	publicBucketName    string
	endpointBaseURL     string
	publicBucketBaseURL string
}

func NewR2Service(cfg config.Config) (*R2Service, error) {
	hasAnyConfig := cfg.R2PrivateBucketName != "" ||
		cfg.R2PublicBucketName != "" ||
		cfg.R2AccessKeyID != "" ||
		cfg.R2SecretAccessKey != "" ||
		cfg.R2Endpoint != "" ||
		cfg.R2AccountID != ""

	if !hasAnyConfig {
		return nil, nil
	}

	endpoint := strings.TrimSpace(cfg.R2Endpoint)
	if endpoint == "" && cfg.R2AccountID != "" {
		endpoint = fmt.Sprintf("https://%s.r2.cloudflarestorage.com", cfg.R2AccountID)
	}

	if endpoint == "" || cfg.R2AccessKeyID == "" || cfg.R2SecretAccessKey == "" {
		return nil, errors.New("R2 endpoint and credentials are required when R2 is configured")
	}

	awsCfg := aws.Config{
		Region:       "auto",
		BaseEndpoint: aws.String(endpoint),
		Credentials: credentials.NewStaticCredentialsProvider(
			cfg.R2AccessKeyID,
			cfg.R2SecretAccessKey,
			"",
		),
	}

	client := s3.NewFromConfig(awsCfg, func(options *s3.Options) {
		options.UsePathStyle = true
	})

	return &R2Service{
		client:              client,
		presignClient:       s3.NewPresignClient(client),
		privateBucketName:   cfg.R2PrivateBucketName,
		publicBucketName:    cfg.R2PublicBucketName,
		endpointBaseURL:     normalizeBaseURL(endpoint),
		publicBucketBaseURL: normalizeBaseURL(cfg.R2PublicBucketBaseURL),
	}, nil
}

func (s *R2Service) PrivateBucketName() string {
	return s.privateBucketName
}

func (s *R2Service) PublicBucketName() string {
	return s.publicBucketName
}

func (s *R2Service) Upload(ctx context.Context, content []byte, objectKey, contentType string, bucketName ...string) (string, error) {
	resolvedBucket, err := s.requireBucketName(bucketName...)
	if err != nil {
		return "", err
	}

	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(resolvedBucket),
		Key:         aws.String(objectKey),
		Body:        bytes.NewReader(content),
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return "", fmt.Errorf("put object: %w", err)
	}

	return s.GetStorageObjectURL(objectKey, resolvedBucket)
}

func (s *R2Service) UploadReader(ctx context.Context, reader io.Reader, objectKey, contentType string, bucketName ...string) (string, error) {
	resolvedBucket, err := s.requireBucketName(bucketName...)
	if err != nil {
		return "", err
	}

	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(resolvedBucket),
		Key:         aws.String(objectKey),
		Body:        reader,
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return "", fmt.Errorf("put object: %w", err)
	}

	return s.GetStorageObjectURL(objectKey, resolvedBucket)
}

func (s *R2Service) ObjectExists(ctx context.Context, objectKey string, bucketName ...string) (bool, error) {
	resolvedBucket, err := s.requireBucketName(bucketName...)
	if err != nil {
		return false, err
	}

	_, err = s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(resolvedBucket),
		Key:    aws.String(objectKey),
	})
	if err == nil {
		return true, nil
	}

	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && apiErr.ErrorCode() == "NotFound" {
		return false, nil
	}

	return false, fmt.Errorf("head object: %w", err)
}

func (s *R2Service) Delete(ctx context.Context, objectKey string, bucketName ...string) error {
	resolvedBucket, err := s.requireBucketName(bucketName...)
	if err != nil {
		return err
	}

	_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(resolvedBucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		return fmt.Errorf("delete object: %w", err)
	}

	return nil
}

func (s *R2Service) Download(ctx context.Context, objectKey string, bucketName ...string) ([]byte, error) {
	return s.DownloadLimited(ctx, objectKey, 0, bucketName...)
}

func (s *R2Service) DownloadLimited(ctx context.Context, objectKey string, maxBytes int64, bucketName ...string) ([]byte, error) {
	resolvedBucket, err := s.requireBucketName(bucketName...)
	if err != nil {
		return nil, err
	}

	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(resolvedBucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		return nil, fmt.Errorf("get object: %w", err)
	}
	defer output.Body.Close()
	if maxBytes > 0 && output.ContentLength != nil && *output.ContentLength > maxBytes {
		return nil, fmt.Errorf("object exceeds the %d-byte limit", maxBytes)
	}

	reader := io.Reader(output.Body)
	if maxBytes > 0 {
		reader = io.LimitReader(output.Body, maxBytes+1)
	}
	content, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read object body: %w", err)
	}
	if maxBytes > 0 && int64(len(content)) > maxBytes {
		return nil, fmt.Errorf("object exceeds the %d-byte limit", maxBytes)
	}

	return content, nil
}

func (s *R2Service) DownloadURL(ctx context.Context, fullURL string) ([]byte, error) {
	parsed, ok := s.ParseStorageURL(fullURL)
	if !ok {
		return nil, errors.New("expected R2 URL")
	}

	return s.Download(ctx, parsed.ObjectKey, parsed.BucketName)
}

func (s *R2Service) SignURL(ctx context.Context, fullURL string, expiry time.Duration) (string, error) {
	parsed, ok := s.ParseStorageURL(fullURL)
	if !ok {
		return "", errors.New("expected R2 URL")
	}

	if s.isPublicBucket(parsed.BucketName) {
		return fullURL, nil
	}

	if expiry <= 0 {
		expiry = 15 * time.Minute
	}

	presigned, err := s.presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(parsed.BucketName),
		Key:    aws.String(parsed.ObjectKey),
	}, func(options *s3.PresignOptions) {
		options.Expires = expiry
	})
	if err != nil {
		return "", fmt.Errorf("presign get object: %w", err)
	}

	return presigned.URL, nil
}

func (s *R2Service) ResolveBrowserURL(ctx context.Context, fullURL string, expiry time.Duration) (string, error) {
	trimmed := strings.TrimSpace(fullURL)
	if trimmed == "" {
		return "", nil
	}
	if s == nil {
		return trimmed, nil
	}

	parsed, ok := s.ParseStorageURL(trimmed)
	if !ok || s.isPublicBucket(parsed.BucketName) {
		return trimmed, nil
	}

	return s.SignURL(ctx, trimmed, expiry)
}

func (s *R2Service) GetStorageObjectURL(objectKey string, bucketName ...string) (string, error) {
	resolvedBucket, err := s.requireBucketName(bucketName...)
	if err != nil {
		return "", err
	}

	encodedKey := encodeObjectKey(objectKey)

	switch {
	case s.isPublicBucket(resolvedBucket) && s.publicBucketBaseURL != "":
		return fmt.Sprintf("%s/%s", s.publicBucketBaseURL, encodedKey), nil
	case s.endpointBaseURL != "":
		return fmt.Sprintf("%s/%s/%s", s.endpointBaseURL, resolvedBucket, encodedKey), nil
	default:
		return "", errors.New("R2 endpoint is not configured")
	}
}

type ParsedStorageURL struct {
	BucketName string
	ObjectKey  string
}

func (s *R2Service) ParseStorageURL(fullURL string) (ParsedStorageURL, bool) {
	urlObj, err := url.Parse(fullURL)
	if err != nil {
		return ParsedStorageURL{}, false
	}

	base := fmt.Sprintf("%s://%s", urlObj.Scheme, urlObj.Host)
	trimmedPath := strings.TrimPrefix(urlObj.EscapedPath(), "/")

	switch {
	case s.publicBucketBaseURL != "" && strings.HasPrefix(fullURL, s.publicBucketBaseURL+"/"):
		if s.publicBucketName == "" {
			return ParsedStorageURL{}, false
		}
		return ParsedStorageURL{
			BucketName: s.publicBucketName,
			ObjectKey:  decodeObjectKey(strings.TrimPrefix(fullURL, s.publicBucketBaseURL+"/")),
		}, true
	case s.endpointBaseURL != "" && base == s.endpointBaseURL:
		parts := strings.Split(trimmedPath, "/")
		if len(parts) < 2 {
			return ParsedStorageURL{}, false
		}
		return ParsedStorageURL{
			BucketName: parts[0],
			ObjectKey:  decodeObjectKey(strings.Join(parts[1:], "/")),
		}, true
	case strings.HasSuffix(urlObj.Hostname(), ".r2.dev"):
		bucketName := strings.Split(urlObj.Hostname(), ".")[0]
		if bucketName == "" || trimmedPath == "" {
			return ParsedStorageURL{}, false
		}
		return ParsedStorageURL{
			BucketName: bucketName,
			ObjectKey:  decodeObjectKey(trimmedPath),
		}, true
	default:
		return ParsedStorageURL{}, false
	}
}

func (s *R2Service) requireBucketName(bucketName ...string) (string, error) {
	if len(bucketName) > 0 && strings.TrimSpace(bucketName[0]) != "" {
		return strings.TrimSpace(bucketName[0]), nil
	}
	if s.privateBucketName != "" {
		return s.privateBucketName, nil
	}
	return "", errors.New("R2 private bucket name is required")
}

func (s *R2Service) isPublicBucket(bucketName string) bool {
	return s.publicBucketName != "" && bucketName == s.publicBucketName
}

func normalizeBaseURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	return strings.TrimRight(trimmed, "/")
}

func encodeObjectKey(objectKey string) string {
	parts := strings.Split(objectKey, "/")
	for index, part := range parts {
		parts[index] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func decodeObjectKey(objectKey string) string {
	parts := strings.Split(objectKey, "/")
	for index, part := range parts {
		if decoded, err := url.PathUnescape(part); err == nil {
			parts[index] = decoded
		}
	}
	return path.Clean(strings.Join(parts, "/"))
}
