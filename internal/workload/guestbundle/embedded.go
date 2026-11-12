package guestbundle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
)

// MaxManifestBytes bounds decoding before the extractor validates file counts.
const MaxManifestBytes = 2 << 20

// ExtractFS loads the fixed manifest.json and assets.tar.gz pair from a trusted
// build-time filesystem, such as embed.FS (or fs.Sub of one). The filesystem must
// not be participant-controlled. There is deliberately no download, host binary
// lookup, platform fallback, or execution here. Missing assets fail closed.
// The caller owns the returned bundle's lifetime, as with Extract.
func ExtractFS(ctx context.Context, parent string, assets fs.FS) (*Bundle, error) {
	if ctx == nil || assets == nil {
		return nil, errors.New("guest bundle context and filesystem required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	manifest, err := openAsset(assets, "manifest.json", MaxManifestBytes)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(contextReader{ctx, manifest}, MaxManifestBytes+1))
	closeErr := manifest.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	if len(data) > MaxManifestBytes {
		return nil, errors.New("guest bundle manifest too large")
	}
	var m Manifest
	// Reject unknown fields and multiple JSON values, rather than silently using
	// metadata intended for a different loader version.
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("trailing guest bundle manifest data")
	}
	archive, err := openAsset(assets, "assets.tar.gz", MaxArchiveBytes)
	if err != nil {
		return nil, err
	}
	bundle, extractErr := Extract(ctx, parent, archive, m)
	closeErr = archive.Close()
	if err := errors.Join(extractErr, closeErr); err != nil {
		if bundle != nil {
			err = errors.Join(err, bundle.Close())
		}
		return nil, err
	}
	return bundle, nil
}

func openAsset(assets fs.FS, name string, limit int64) (fs.File, error) {
	file, err := assets.Open(name)
	if err != nil {
		return nil, err
	}
	info, statErr := file.Stat()
	if statErr != nil {
		return nil, errors.Join(statErr, file.Close())
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > limit {
		return nil, errors.Join(errors.New("invalid guest bundle asset type or size"), file.Close())
	}
	return file, nil
}
