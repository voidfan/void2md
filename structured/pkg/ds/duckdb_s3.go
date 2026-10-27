package ds

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

// S3StoreOptions are the parameters of DuckdbS3Store.
type S3StoreOptions struct {
	Host      string
	Port      int
	AccessKey string
	SecretKey string
	Bucket    string
	Region    string
	UseSSL    bool
	// KeyPrefix: CreateS3StoreFromEnv passes "dataset/{kid}/"; direct
	// construction leaves it empty.
	KeyPrefix string
}

// DuckdbS3Store reads files from an S3-compatible bucket.
type DuckdbS3Store struct {
	duckdbBase

	Endpoint    string
	EndpointURL string
	AccessKey   string
	SecretKey   string
	Bucket      string
	Region      string
	UseSSL      bool
	KeyPrefix   string

	s3 *s3.Client
}

// NewDuckdbS3Store builds an S3 store; it creates the client and connects at
// construction time, like Python.
func NewDuckdbS3Store(opts S3StoreOptions) (DuckdbStore, error) {
	endpoint := fmt.Sprintf("%s:%d", opts.Host, opts.Port)
	scheme := "http"
	if opts.UseSSL {
		scheme = "https"
	}
	store := &DuckdbS3Store{
		Endpoint:    endpoint,
		EndpointURL: scheme + "://" + endpoint,
		AccessKey:   opts.AccessKey,
		SecretKey:   opts.SecretKey,
		Bucket:      opts.Bucket,
		Region:      opts.Region,
		UseSSL:      opts.UseSSL,
		KeyPrefix:   opts.KeyPrefix,
		duckdbBase: duckdbBase{
			DBName: opts.Bucket,
		},
	}
	store.impl = store
	client, err := store.newClient(context.Background())
	if err != nil {
		return nil, err
	}
	store.s3 = client
	if _, err := store.getConn(); err != nil {
		return nil, err
	}
	return store, nil
}

// newClient creates the S3 client with static credentials.
func (s *DuckdbS3Store) newClient(ctx context.Context) (*s3.Client, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(s.Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			s.AccessKey, s.SecretKey, "",
		)),
	)
	if err != nil {
		return nil, err
	}
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(s.EndpointURL)
		// Addressing style is left at the SDK default, as Python does.
	}), nil
}

// getConn opens an in-memory duckdb connection, loads the excel and httpfs
// extensions, and creates the S3 SECRET.
func (s *DuckdbS3Store) getConn() (duckdbConn, error) {
	conn, err := openDuckDBConn("")
	if err != nil {
		return nil, err
	}
	if err := duckdbLoadExtension(conn, "excel"); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := duckdbLoadExtension(conn, "httpfs"); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := conn.Exec(s.secretSQL()); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

// secretSQL builds the CREATE SECRET statement. Python quirk preserved: the
// region goes through repr (!r), so a non-empty region is stored double-quoted
// (e.g. `'us-east-1'`).
func (s *DuckdbS3Store) secretSQL() string {
	useSSL := "false"
	if s.UseSSL {
		useSSL = "true"
	}
	return fmt.Sprintf(`
CREATE OR REPLACE SECRET secret (
    TYPE s3,
    ENDPOINT '%s',
    KEY_ID '%s',
    SECRET '%s',
    USE_SSL %s,
    URL_STYLE 'path',
    REGION '%s',
    SCOPE 's3://%s'
);
`, s.Endpoint, s.AccessKey, s.SecretKey, useSSL, pythonRepr(s.Region), s.Bucket)
}

// pythonRepr mirrors Python's repr(str) quoting rules.
func pythonRepr(s string) string {
	quote := "'"
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		quote = `"`
	}
	var b strings.Builder
	b.WriteString(quote)
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if string(r) == quote {
				b.WriteString(`\`)
			}
			b.WriteRune(r)
		}
	}
	b.WriteString(quote)
	return b.String()
}

// connTest returns true when head_bucket succeeds, false on an API error.
func (s *DuckdbS3Store) connTest(ctx context.Context) (bool, error) {
	_, err := s.s3.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.Bucket)})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// getFiles lists the bucket: duckdb-supported extensions get an s3:// path,
// fastexcel extensions get their body read into Data.
func (s *DuckdbS3Store) getFiles(ctx context.Context) ([]*FileInfo, error) {
	files := []*FileInfo{}
	paginator := s3.NewListObjectsV2Paginator(s.s3, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.Bucket),
		Prefix: prefixOrNil(s.KeyPrefix),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, obj := range page.Contents {
			key := aws.ToString(obj.Key)
			if s.KeyPrefix != "" && strings.HasPrefix(key, s.KeyPrefix) {
				key = key[len(s.KeyPrefix):]
			}
			// Python: key.rsplit(".", 1)
			name := rsplitOnce(key, ".")
			extension := strings.ToLower(rsplitLast(key, "."))
			if duckdbSupportedExtensions[extension] {
				files = append(files, &FileInfo{
					Name:      name,
					Extension: extension,
					Path:      fmt.Sprintf("s3://%s/%s", s.Bucket, aws.ToString(obj.Key)),
				})
				continue
			}
			if fastExcelSupportedExtensions[extension] {
				out, err := s.s3.GetObject(ctx, &s3.GetObjectInput{
					Bucket: aws.String(s.Bucket),
					Key:    obj.Key,
				})
				if err != nil {
					return nil, err
				}
				content, err := io.ReadAll(out.Body)
				_ = out.Body.Close()
				if err != nil {
					return nil, err
				}
				files = append(files, &FileInfo{
					Name:      name,
					Extension: extension,
					Data:      content,
				})
				continue
			}
		}
	}
	return files, nil
}

// insertLocalData uploads a local file by path.
func (s *DuckdbS3Store) insertLocalData(ctx context.Context, filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	return s.insertData(ctx, filepathBase(filePath), f, nil)
}

// insertData uploads bytes under KeyPrefix+filename, creating the bucket on a
// 404. Returns the S3 key.
func (s *DuckdbS3Store) insertData(
	ctx context.Context, filename string, file io.Reader, meta map[string]any,
) (string, error) {
	s3Key := filename
	if s.KeyPrefix != "" {
		s3Key = s.KeyPrefix + filename
	}
	if _, err := s.s3.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.Bucket)}); err != nil {
		var apiErr smithy.APIError
		if !errors.As(err, &apiErr) {
			return "", err
		}
		code, convErr := strconv.Atoi(apiErr.ErrorCode())
		if convErr != nil {
			// Python: int(code) raises ValueError, which bubbles up.
			return "", fmt.Errorf("invalid literal for int() with base 10: '%s'", apiErr.ErrorCode())
		}
		if code == 404 {
			_, _ = s.s3.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(s.Bucket)})
		}
	}
	// Python: Body=file.read()
	body, err := io.ReadAll(file)
	if err != nil {
		return s3Key, err
	}
	_, err = s.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.Bucket),
		Key:    aws.String(s3Key),
		Body:   bytes.NewReader(body),
	})
	if err != nil {
		return s3Key, err
	}
	return s3Key, nil
}

// deleteData deletes the object; API errors are swallowed.
func (s *DuckdbS3Store) deleteData(ctx context.Context, filename string) error {
	s3Key := filename
	if s.KeyPrefix != "" {
		s3Key = s.KeyPrefix + filename
	}
	_, err := s.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.Bucket),
		Key:    aws.String(s3Key),
	})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			return nil
		}
		return err
	}
	return nil
}

// CreateS3StoreFromEnv builds an S3 store from STORAGE_* environment
// variables; returns nil when any required variable is empty.
func CreateS3StoreFromEnv(kid string) (DuckdbStore, error) {
	endpoint := os.Getenv("STORAGE_S3_ENDPOINT")
	accessKey := os.Getenv("STORAGE_ACCESS_KEY_ID")
	secretKey := os.Getenv("STORAGE_SECRET_ACCESS_KEY")
	bucket := os.Getenv("STORAGE_PRIVATE_BUCKET")
	if endpoint == "" || accessKey == "" || secretKey == "" || bucket == "" {
		return nil, nil
	}

	useSSL := strings.HasPrefix(endpoint, "https://")
	stripped := strings.ReplaceAll(strings.ReplaceAll(endpoint, "https://", ""), "http://", "")
	host := stripped
	port := 80
	if useSSL {
		port = 443
	}
	if idx := strings.LastIndex(stripped, ":"); idx >= 0 {
		p, err := strconv.Atoi(stripped[idx+1:])
		if err != nil {
			// Python: int(port_str) raises ValueError, which bubbles up.
			return nil, err
		}
		host = stripped[:idx]
		port = p
	}

	keyPrefix := ""
	if kid != "" {
		keyPrefix = "dataset/" + kid + "/"
	}
	return NewDuckdbS3Store(S3StoreOptions{
		Host:      host,
		Port:      port,
		AccessKey: accessKey,
		SecretKey: secretKey,
		Bucket:    bucket,
		UseSSL:    useSSL,
		KeyPrefix: keyPrefix,
	})
}

// --- Small helpers ---

func prefixOrNil(prefix string) *string {
	if prefix == "" {
		return nil
	}
	return aws.String(prefix)
}

// rsplitOnce mirrors Python's `key.rsplit(".", 1)[0]` (the original string is
// returned when there is no dot).
func rsplitOnce(key, sep string) string {
	idx := strings.LastIndex(key, sep)
	if idx < 0 {
		return key
	}
	return key[:idx]
}

// rsplitLast mirrors Python's `key.rsplit(".", 1)[-1]` (the original string is
// returned when there is no dot).
func rsplitLast(key, sep string) string {
	idx := strings.LastIndex(key, sep)
	if idx < 0 {
		return key
	}
	return key[idx+len(sep):]
}

func filepathBase(p string) string {
	if idx := strings.LastIndexAny(p, `/\`); idx >= 0 {
		return p[idx+1:]
	}
	return p
}
