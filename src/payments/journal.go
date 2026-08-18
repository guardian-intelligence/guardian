package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type recoveryJournal interface {
	WriteIntent(context.Context, string, any) error
	WriteOutcome(context.Context, string, any) error
}

type s3API interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
}

type s3Journal struct {
	client s3API
	bucket string
	prefix string
}

func newS3Journal(ctx context.Context, cfg config) (*s3Journal, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(
		ctx,
		awsconfig.WithRegion(cfg.JournalRegion),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			cfg.JournalAccessKeyID,
			cfg.JournalSecretAccessKey,
			"",
		)),
	)
	if err != nil {
		return nil, fmt.Errorf("load R2 client config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(cfg.JournalEndpoint)
		options.UsePathStyle = true
	})
	return &s3Journal{
		client: client,
		bucket: cfg.JournalBucket,
		prefix: cfg.JournalPrefix,
	}, nil
}

func (j *s3Journal) WriteIntent(ctx context.Context, commandID string, value any) error {
	return j.writeOnce(ctx, commandID, "intent", value)
}

func (j *s3Journal) WriteOutcome(ctx context.Context, commandID string, value any) error {
	return j.writeOnce(ctx, commandID, "outcome", value)
}

func (j *s3Journal) writeOnce(
	ctx context.Context,
	commandID string,
	kind string,
	value any,
) error {
	body, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal journal %s: %w", kind, err)
	}
	digestBytes := sha256.Sum256(body)
	digest := hex.EncodeToString(digestBytes[:])
	key := path.Join(
		j.prefix,
		"commands",
		commandID,
		kind+".json",
	)
	_, err = j.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(j.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String("application/json"),
		IfNoneMatch: aws.String("*"),
		Metadata: map[string]string{
			"guardian-sha256": digest,
			"guardian-kind":   kind,
			"guardian-ledger": "2",
		},
	})
	if err == nil {
		return nil
	}
	head, headErr := j.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(j.bucket),
		Key:    aws.String(key),
	})
	if headErr != nil {
		return fmt.Errorf("write journal %s: %w", kind, err)
	}
	if head.Metadata["guardian-sha256"] != digest {
		return fmt.Errorf("journal %s already exists with different digest", kind)
	}
	return nil
}

type memoryJournal struct {
	intents  map[string][]byte
	outcomes map[string][]byte
}

func newMemoryJournal() *memoryJournal {
	return &memoryJournal{
		intents:  make(map[string][]byte),
		outcomes: make(map[string][]byte),
	}
}

func (j *memoryJournal) WriteIntent(_ context.Context, id string, value any) error {
	return writeMemoryRecord(j.intents, id, value)
}

func (j *memoryJournal) WriteOutcome(_ context.Context, id string, value any) error {
	return writeMemoryRecord(j.outcomes, id, value)
}

func writeMemoryRecord(target map[string][]byte, id string, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if existing, ok := target[id]; ok && !bytes.Equal(existing, body) {
		return fmt.Errorf("journal record %s changed", id)
	}
	target[id] = body
	return nil
}
