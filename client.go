package internalformat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"gocloud.dev/blob"
)

// Client provides read access to forecast data stored in a blob bucket using
// the internal format. Use [NewClient] or [NewClientFromBucket] to obtain an
// instance.
type Client interface {
	// Close releases any resources held by the client. It must be called when
	// the client is no longer needed.
	Close() error
	// Latest returns the latest version number for each known area. The map
	// key is the area identifier and the value is its current version.
	Latest(ctx context.Context) (map[string]int, error)
	// GetMeta fetches the dataset metadata for a specific area and version.
	GetMeta(ctx context.Context, area string, version int) (*DatasetMeta, error)
	// GetGridInfo lists the grids available in a dataset, along with the raw
	// size of each grid's data blob.
	GetGridInfo(ctx context.Context, d *DatasetMeta) ([]GridInfo, error)
	// GetGridMeta fetches the parameter metadata for a specific grid within a
	// dataset.
	GetGridMeta(ctx context.Context, d *DatasetMeta, gridid string) (*MetaCollection, error)
	// GetData opens a streaming reader for the raw forecast data of a grid.
	GetData(ctx context.Context, d *DatasetMeta, gridid string) (DataReader, error)
	// GetDataRange opens a reader for a byte range within a grid's data blob.
	// from is the start offset and length is the number of bytes to read.
	GetDataRange(ctx context.Context, d *DatasetMeta, hash string, from, length int) (io.ReadCloser, error)
	// GetLatitude opens a streaming reader for the latitude coordinates of a
	// grid.
	GetLatitude(ctx context.Context, d *DatasetMeta, gridid string) (DataReader, error)
	// GetLongitude opens a streaming reader for the longitude coordinates of a
	// grid.
	GetLongitude(ctx context.Context, d *DatasetMeta, gridid string) (DataReader, error)
}

type client struct {
	bucket *blob.Bucket
}

// NewClient opens a blob bucket at connectURL and returns a Client backed by
// it. The URL format is determined by the gocloud.dev/blob driver in use
// (e.g. "s3://my-bucket", "gs://my-bucket", "azblob://my-container").
// The connection attempt times out after 5 seconds.
func NewClient(connectURL string) (Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	bucket, err := blob.OpenBucket(ctx, connectURL)
	if err != nil {
		return nil, err
	}

	return NewClientFromBucket(bucket), nil
}

// NewClientFromBucket returns a Client backed by an already-open blob.Bucket.
// Useful in tests or when the caller manages the bucket lifecycle directly.
func NewClientFromBucket(bucket *blob.Bucket) Client {
	return &client{bucket}
}

func (c *client) Close() error {
	return c.bucket.Close()
}

func (c *client) Latest(ctx context.Context) (map[string]int, error) {
	prefix := "latest/"

	ret := make(map[string]int)
	it := c.bucket.List(&blob.ListOptions{
		Prefix: prefix,
	})
	for {
		lo, err := it.Next(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		area := strings.TrimPrefix(lo.Key, prefix)

		r, err := c.bucket.NewReader(ctx, lo.Key, nil)
		if err != nil {
			return nil, err
		}
		defer r.Close()

		var version int
		if _, err := fmt.Fscanf(r, "%d", &version); err != nil {
			return nil, err
		}
		ret[area] = version
	}
	return ret, nil
}

func (c *client) GetMeta(ctx context.Context, area string, version int) (*DatasetMeta, error) {
	path := fmt.Sprintf("%s/%d/complete.json", area, version)

	r, err := c.bucket.NewReader(ctx, path, nil)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	var ret DatasetMeta
	if err := json.NewDecoder(r).Decode(&ret); err != nil {
		return nil, err
	}

	return &ret, nil
}

// GridInfo describes a single grid within a dataset.
type GridInfo struct {
	// ID is the grid identifier (the path segment between the version and the
	// file name in the bucket layout).
	ID string
	// RawDataSize is the size in bytes of the raw data blob for this grid.
	RawDataSize int64
}

func (c *client) GetGridInfo(ctx context.Context, d *DatasetMeta) ([]GridInfo, error) {
	var gridids []GridInfo
	it := c.bucket.List(
		&blob.ListOptions{
			Prefix:    fmt.Sprintf("%s/%d/", d.Area, d.Version),
			Delimiter: "/",
		},
	)
	for {
		obj, err := it.Next(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		if obj.IsDir {
			elements := strings.Split(obj.Key, "/")
			if len(elements) != 4 {
				continue
			}

			attr, err := c.bucket.Attributes(ctx, obj.Key+"data")
			if err != nil {
				return nil, err
			}

			gridids = append(gridids,
				GridInfo{
					ID:          elements[2],
					RawDataSize: attr.Size,
				},
			)
		}
	}

	return gridids, nil
}

func (c *client) GetGridMeta(ctx context.Context, d *DatasetMeta, gridid string) (*MetaCollection, error) {
	path := fmt.Sprintf("%s/%d/%s/meta.json", d.Area, d.Version, gridid)

	r, err := c.bucket.NewReader(ctx, path, nil)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	var ret MetaCollection
	if err := json.NewDecoder(r).Decode(&ret); err != nil {
		return nil, err
	}

	return &ret, nil
}

// DataReader is a streaming reader that also exposes the total byte size of
// the underlying blob. Callers must call Close when done.
type DataReader interface {
	io.ReadCloser
	Size() int64
}

func (c *client) GetData(ctx context.Context, d *DatasetMeta, gridid string) (DataReader, error) {
	return c.getStream(ctx, fmt.Sprintf("%s/%d/%s/data", d.Area, d.Version, gridid))
}

func (c *client) GetDataRange(ctx context.Context, d *DatasetMeta, hash string, from, length int) (io.ReadCloser, error) {
	path := fmt.Sprintf("%s/%d/%s/data", d.Area, d.Version, hash)
	r, err := c.bucket.NewRangeReader(ctx, path, int64(from), int64(length), nil)
	if err != nil {
		return nil, err
	}

	return r, nil
}

func (c *client) GetLatitude(ctx context.Context, d *DatasetMeta, gridid string) (DataReader, error) {
	return c.getStream(ctx, fmt.Sprintf("%s/%d/%s/latitude", d.Area, d.Version, gridid))
}

func (c *client) GetLongitude(ctx context.Context, d *DatasetMeta, gridid string) (DataReader, error) {
	return c.getStream(ctx, fmt.Sprintf("%s/%d/%s/longitude", d.Area, d.Version, gridid))
}

func (c *client) getStream(ctx context.Context, path string) (DataReader, error) {
	r, err := c.bucket.NewReader(ctx, path, nil)
	if err != nil {
		return nil, err
	}

	return r, nil
}
