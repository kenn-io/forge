package platform

import (
	"context"
	"errors"
)

type ArchivePageFetcher[T any] func(context.Context, string) (ArchivePage[T], error)

func CollectArchivePages[T any](
	ctx context.Context,
	cursor string,
	fetch ArchivePageFetcher[T],
) ([]T, error) {
	var items []T
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := fetch(ctx, cursor)
		if err != nil {
			return nil, err
		}
		if page.Exhausted {
			return append(items, page.Items...), nil
		}
		if page.NextCursor == "" {
			return nil, errors.New("archive page did not return a next cursor or exhaustion")
		}
		if page.NextCursor == cursor {
			return nil, errors.New("archive page repeated its cursor")
		}
		items = append(items, page.Items...)
		cursor = page.NextCursor
	}
}
