// Package storage bina presigned URL untuk upload terus ke Cloudflare R2
// (S3-compatible) — client upload gambar terus ke R2, Go backend tak
// pernah sentuh bytes gambar tu langsung (elak jadi bottleneck bandwidth).
package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
)

const (
	presignExpiry = 5 * time.Minute

	// MaxImageSizeBytes — had saiz setiap gambar post (5 MB, padanan had
	// klasik Twitter — munasabah untuk upload mobile).
	//
	// Nota: R2 TAK support presigned POST (`content-length-range` policy
	// condition macam S3 sebenar) — verified terus: "Presigned post
	// requests are not yet implemented" (501) bila cuba. Jadi had ni
	// dikuatkuasakan LEPAS upload via HeadObject (VerifyImageSize), bukan
	// dihalang di peringkat presign macam yang dirancang asalnya.
	MaxImageSizeBytes = 5 * 1024 * 1024

	// MaxImagesPerPost — had bilangan gambar setiap post.
	MaxImagesPerPost = 4
)

var ErrImageTooLarge = errors.New("gambar melebihi had saiz")
var ErrImageInvalidFormat = errors.New("format gambar tidak sah")

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
		// aws-sdk-go-v2 default (WhenSupported) auto-tambah checksum
		// CRC32 pada setiap request S3, termasuk presigned URL — R2
		// tak fully compatible dgn ni, signature jadi tak sah, PUT
		// client dapat 403 AccessDenied walaupun presign sendiri
		// (langkah GENERATE URL) nampak berjaya. Verified: tanpa
		// override ni, upload sebenar ke R2 gagal 403; dengan ni, OK.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
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

// VerifyImageSize semak saiz objek yang DAH diupload (HeadObject) tak
// melebihi MaxImageSizeBytes. Dipanggil dari CreatePost sebelum r2_key
// diterima masuk post — R2 tak support content-length-range di presign
// PUT, jadi ni satu-satunya titik enforcement sebenar (client-side check
// pun ada, tapi cuma UX, bukan security boundary).
func (r *R2Client) VerifyImageSize(ctx context.Context, key string) error {
	if !r.configured {
		return fmt.Errorf("R2 belum configure")
	}

	out, err := r.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(r.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("head object: %w", err)
	}

	if out.ContentLength != nil && *out.ContentLength > MaxImageSizeBytes {
		return ErrImageTooLarge
	}

	return nil
}

// VerifyImageFormat semak byte pertama objek (magic number) padan
// dengan salah satu format imej dibenarkan (JPEG/PNG/WEBP) — Content-
// Type di header PUT boleh dipalsukan client, byte sebenar tak boleh.
// Dipanggil sekali gus dengan VerifyImageSize sebelum r2_key diterima
// masuk post.
func (r *R2Client) VerifyImageFormat(ctx context.Context, key string) error {
	if !r.configured {
		return fmt.Errorf("R2 belum configure")
	}

	out, err := r.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(r.bucket),
		Key:    aws.String(key),
		Range:  aws.String("bytes=0-11"),
	})
	if err != nil {
		return fmt.Errorf("get object: %w", err)
	}
	defer out.Body.Close()

	buf := make([]byte, 12)
	n, _ := io.ReadFull(out.Body, buf)
	buf = buf[:n]

	switch {
	case bytes.HasPrefix(buf, []byte{0xFF, 0xD8, 0xFF}): // JPEG
	case bytes.HasPrefix(buf, []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}): // PNG
	case len(buf) >= 12 && bytes.Equal(buf[0:4], []byte("RIFF")) && bytes.Equal(buf[8:12], []byte("WEBP")): // WEBP
	default:
		return ErrImageInvalidFormat
	}

	return nil
}

// DeleteImage buang objek dari R2 — dipanggil bila gambar ditolak
// (terlalu besar) supaya tak tinggal orphan dalam bucket.
func (r *R2Client) DeleteImage(ctx context.Context, key string) error {
	if !r.configured {
		return nil
	}
	_, err := r.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(r.bucket),
		Key:    aws.String(key),
	})
	return err
}

// HasPublicURL — sama ada domain awam bucket dah dikonfigur. Tanpa ni
// gambar boleh diupload tapi TAK BOLEH dipapar semula: PublicURL pulang
// string kosong untuk setiap kunci.
func (r *R2Client) HasPublicURL() bool {
	return r.publicURL != ""
}

// PublicURL bina URL awam untuk baca semula gambar yang dah diupload
// (r2_key disimpan dalam DB, URL dibina runtime — elak simpan URL penuh
// yang boleh berubah kalau domain public R2 ditukar).
//
// Pulang "" kalau R2_PUBLIC_URL tak diset. Caller MESTI langkau nilai
// kosong dan bukan hantar ia kepada client — lihat buildPostResponses.
func (r *R2Client) PublicURL(key string) string {
	if r.publicURL == "" {
		return ""
	}
	return fmt.Sprintf("%s/%s", r.publicURL, key)
}
