package ds

import (
	"bytes"
	"context"
	"io"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// MongoStoreOptions mirrors the parameters of DuckdbMongoStore.__init__.
type MongoStoreOptions struct {
	URI                      string
	Bucket                   string
	ConnectTimeoutMS         int
	ServerSelectionTimeoutMS int
}

// MongoFastGPTOptions mirrors the parameters of DuckdbMongoStoreForFastGPT.__init__.
type MongoFastGPTOptions struct {
	URI     string
	Kid     string
	Bucket  string
	FileIDs []string
}

// DuckdbMongoStore reads files from MongoDB GridFS ({bucket}.files /
// {bucket}.chunks).
type DuckdbMongoStore struct {
	duckdbBase

	Client    *mongo.Client
	Db        *mongo.Database
	Bucket    *mongo.GridFSBucket
	BucketNam string
}

// NewDuckdbMongoStore builds a Mongo store; bucket defaults to "dataset" and
// timeouts to 5000ms. It connects at construction, like Python.
func NewDuckdbMongoStore(opts MongoStoreOptions) (*DuckdbMongoStore, error) {
	if opts.Bucket == "" {
		opts.Bucket = "dataset"
	}
	connectTimeout := opts.ConnectTimeoutMS
	if connectTimeout == 0 {
		connectTimeout = 5000
	}
	selectionTimeout := opts.ServerSelectionTimeoutMS
	if selectionTimeout == 0 {
		selectionTimeout = 5000
	}
	client, err := mongo.Connect(options.Client().
		ApplyURI(opts.URI).
		SetConnectTimeout(time.Duration(connectTimeout) * time.Millisecond).
		SetServerSelectionTimeout(time.Duration(selectionTimeout) * time.Millisecond))
	if err != nil {
		return nil, err
	}
	store := &DuckdbMongoStore{
		Client: client,
		// Python: self.client.get_default_database()
		Db:        client.Database(""),
		BucketNam: opts.Bucket,
		duckdbBase: duckdbBase{
			DBName: opts.Bucket,
		},
	}
	store.Bucket = store.Db.GridFSBucket(options.GridFSBucket().SetName(opts.Bucket))
	store.impl = store
	if _, err := store.getConn(); err != nil {
		return nil, err
	}
	return store, nil
}

// getConn opens a plain in-memory duckdb connection (no extensions).
func (s *DuckdbMongoStore) getConn() (duckdbConn, error) {
	return openDuckDBConn("")
}

// connTest passes only when both {bucket}.files and {bucket}.chunks exist.
func (s *DuckdbMongoStore) connTest(ctx context.Context) (bool, error) {
	names, err := s.Db.ListCollectionNames(ctx, bson.D{})
	if err != nil {
		return false, err
	}
	has := func(want string) bool {
		for _, n := range names {
			if n == want {
				return true
			}
		}
		return false
	}
	return has(s.BucketNam+".files") && has(s.BucketNam+".chunks"), nil
}

// getFilesWithIDs fetches GridFS files by _id; name is the first dot-separated
// segment of the filename and extension the last (both lowercased). Empty
// fileIDs returns an empty list.
func (s *DuckdbMongoStore) getFilesWithIDs(ctx context.Context, fileIDs []string) ([]*FileInfo, error) {
	if len(fileIDs) == 0 {
		return []*FileInfo{}, nil
	}
	ids := make([]any, 0, len(fileIDs))
	for _, id := range fileIDs {
		oid, err := bson.ObjectIDFromHex(id)
		if err != nil {
			return nil, err
		}
		ids = append(ids, oid)
	}
	cursor, err := s.Bucket.Find(ctx, bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: ids}}}})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	files := []*FileInfo{}
	for cursor.Next(ctx) {
		var doc struct {
			ID       bson.ObjectID `bson:"_id"`
			Filename string        `bson:"filename"`
		}
		if err := cursor.Decode(&doc); err != nil {
			return nil, err
		}
		filename := doc.Filename
		if filename == "" {
			continue
		}
		stream, err := s.Bucket.OpenDownloadStream(ctx, doc.ID)
		if err != nil {
			return nil, err
		}
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(stream); err != nil {
			_ = stream.Close()
			return nil, err
		}
		_ = stream.Close()
		data := buf.Bytes()

		// Python: filename.split(".")[0].lower() / filename.split(".")[-1].lower()
		name := strings.ToLower(splitFirst(filename, "."))
		extension := strings.ToLower(splitLast(filename, "."))
		if len(data) > 0 && supportedExtensions[extension] {
			files = append(files, &FileInfo{
				Name:      name,
				Extension: extension,
				Data:      data,
			})
		}
	}
	if err := cursor.Err(); err != nil {
		return nil, err
	}
	return files, nil
}

// getFiles returns an empty list (Python's no-file_ids branch).
func (s *DuckdbMongoStore) getFiles(ctx context.Context) ([]*FileInfo, error) {
	return s.getFilesWithIDs(ctx, nil)
}

// insertData uploads via GridFS and returns the ObjectId hex.
func (s *DuckdbMongoStore) insertData(
	ctx context.Context, filename string, file io.Reader, meta map[string]any,
) (string, error) {
	opts := []options.Lister[options.GridFSUploadOptions]{}
	if meta != nil {
		opts = append(opts, options.GridFSUpload().SetMetadata(meta))
	}
	stream, err := s.Bucket.OpenUploadStream(ctx, filename, opts...)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(stream, file); err != nil {
		_ = stream.Abort()
		return "", err
	}
	if err := stream.Close(); err != nil {
		return "", err
	}
	return stream.FileID.(bson.ObjectID).Hex(), nil
}

// deleteData deletes GridFS entries by filename; errors are swallowed.
func (s *DuckdbMongoStore) deleteData(ctx context.Context, filename string) error {
	cursor, err := s.Bucket.Find(ctx, bson.D{{Key: "filename", Value: filename}})
	if err != nil {
		return nil
	}
	defer cursor.Close(ctx)
	for cursor.Next(ctx) {
		var doc struct {
			ID bson.ObjectID `bson:"_id"`
		}
		if err := cursor.Decode(&doc); err != nil {
			return nil
		}
		if err := s.Bucket.Delete(ctx, doc.ID); err != nil {
			return nil
		}
	}
	return nil
}

// DuckdbMongoStoreForFastGPT is a Mongo store variant that resolves file IDs by
// knowledge-base id (kid).
type DuckdbMongoStoreForFastGPT struct {
	*DuckdbMongoStore

	// Kid mirrors self.kid (an ObjectId).
	Kid bson.ObjectID
	// FileIDs nil means Python's None: IDs are looked up by kid instead.
	FileIDs []string
}

// NewDuckdbMongoStoreForFastGPT builds the FastGPT Mongo store variant.
func NewDuckdbMongoStoreForFastGPT(opts MongoFastGPTOptions) (DuckdbStore, error) {
	base, err := NewDuckdbMongoStore(MongoStoreOptions{
		URI:                      opts.URI,
		Bucket:                   opts.Bucket,
		ConnectTimeoutMS:         5000,
		ServerSelectionTimeoutMS: 5000,
	})
	if err != nil {
		return nil, err
	}
	kid, err := bson.ObjectIDFromHex(opts.Kid)
	if err != nil {
		return nil, err
	}
	store := &DuckdbMongoStoreForFastGPT{
		DuckdbMongoStore: base,
		Kid:              kid,
		FileIDs:          opts.FileIDs,
	}
	// Repoint impl at the wrapper so getFiles dispatches to the override below
	// (matching Python's super() chain).
	base.impl = store
	return store, nil
}

// getFileIDsByKid looks up fileId values in a collection by kid; defaults are
// key "datasetId" and collection "dataset_collections".
func (s *DuckdbMongoStoreForFastGPT) getFileIDsByKid(
	ctx context.Context, key, collectionName string,
) ([]string, error) {
	if key == "" {
		key = "datasetId"
	}
	if collectionName == "" {
		collectionName = "dataset_collections"
	}
	coll := s.Db.Collection(collectionName)
	cursor, err := coll.Find(ctx,
		bson.D{{Key: key, Value: s.Kid}},
		options.Find().SetProjection(bson.D{{Key: "fileId", Value: 1}}),
	)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	fileIDs := []string{}
	for cursor.Next(ctx) {
		var doc struct {
			FileID bson.ObjectID `bson:"fileId"`
		}
		if err := cursor.Decode(&doc); err != nil {
			return nil, err
		}
		fileIDs = append(fileIDs, doc.FileID.Hex())
	}
	if err := cursor.Err(); err != nil {
		return nil, err
	}
	return fileIDs, nil
}

// getFiles uses the configured FileIDs, or looks them up by kid when nil.
func (s *DuckdbMongoStoreForFastGPT) getFiles(ctx context.Context) ([]*FileInfo, error) {
	fileIDs := s.FileIDs
	if fileIDs == nil {
		var err error
		fileIDs, err = s.getFileIDsByKid(ctx, "", "")
		if err != nil {
			return nil, err
		}
	}
	return s.getFilesWithIDs(ctx, fileIDs)
}

// --- Small helpers ---

// splitFirst mirrors Python's `s.split(sep)[0]`.
func splitFirst(s, sep string) string {
	if idx := strings.Index(s, sep); idx >= 0 {
		return s[:idx]
	}
	return s
}

// splitLast mirrors Python's `s.split(sep)[-1]`.
func splitLast(s, sep string) string {
	if idx := strings.LastIndex(s, sep); idx >= 0 {
		return s[idx+len(sep):]
	}
	return s
}
