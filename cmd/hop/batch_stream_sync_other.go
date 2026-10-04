//go:build !darwin

package main

import "context"

func syncStreamBatch(ctx context.Context, files []*pendingStreamFile) error {
	return parallelFilesN(len(files), 8, func(i int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return files[i].sync()
	})
}
