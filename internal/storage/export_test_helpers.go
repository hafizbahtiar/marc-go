package storage

import "github.com/aws/aws-sdk-go-v2/service/s3"

// ExportedClientForTest dedah client S3 dalaman untuk ujian pakej lain
// (cth internal/reaper) yang perlu meletakkan objek probe terus ke bucket.
// Bukan sebahagian API awam — jangan guna dalam kod produksi.
func ExportedClientForTest(r *R2Client) (*s3.Client, string) {
	return r.client, r.bucket
}
