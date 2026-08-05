// Package storage bina presigned URL untuk upload terus ke Cloudflare R2
// (S3-compatible) — client upload gambar terus ke R2, Go backend tak
// pernah sentuh bytes gambar tu langsung (elak jadi bottleneck bandwidth).
package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
)

const presignExpiry = 5 * time.Minute

type R2Client struct {
	client     *s3.Client
	presigner  *s3.PresignClient
	bucket     string
	publicURL  string
	configured bool
}

func NewR2Client(accountID, accessKeyID, secretAccessKey, bucket, publicURL string) *R2Client {
	if accountID == "" || accessKeyID == "" || secretAccessKey == "" || bucket == "" {
		return &R2Client{configured: false}
	}

	endpoint := fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountID)

	client := s3.New(s3.Options{
		Region:       "auto",
		BaseEndpoint: aws.String(endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, ""),
	})

	return &R2Client{
		client:     client,
		presigner:  s3.NewPresignClient(client),
		bucket:     bucket,
		publicURL:  publicURL,
		configured: true,
	}
}

func (r *R2Client) Enabled() bool {
	return r.configured
}

// PresignUpload jana key unik + URL PUT presigned (expire 5 minit) untuk
// client upload terus ke R2.
func (r *R2Client) PresignUpload(ctx context.Context, contentType string) (uploadURL, key string, err error) {
	if !r.configured {
		return "", "", fmt.Errorf("R2 belum configure")
	}

	key = fmt.Sprintf("posts/%s", uuid.New().String())

	req, err := r.presigner.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(r.bucket),
		Key:         aws.String(key),
		ContentType: aws.String(contentType),
	}, s3.WithPresignExpires(presignExpiry))
	if err != nil {
		return "", "", fmt.Errorf("presign: %w", err)
	}

	return req.URL, key, nil
}

// PublicURL bina URL awam untuk baca semula gambar yang dah diupload
// (r2_key disimpan dalam DB, URL dibina runtime — elak simpan URL penuh
// yang boleh berubah kalau domain public R2 ditukar).
func (r *R2Client) PublicURL(key string) string {
	if r.publicURL == "" {
		return ""
	}
	return fmt.Sprintf("%s/%s", r.publicURL, key)
}
