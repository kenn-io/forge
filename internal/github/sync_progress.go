package github

import (
	"log/slog"
)

const (
	syncProgressLogInterval       = 100
	largeRepoBulkGraphQLThreshold = syncProgressLogInterval
)

type itemSyncProgressLogger struct {
	repo   RepoRef
	source string
	item   string
	total  int
}

type listFetchProgressLogger struct {
	repo    RepoRef
	source  string
	item    string
	total   int
	fetched int
	started bool
}

func newIssueSyncProgressLogger(repo RepoRef, source string, total int) itemSyncProgressLogger {
	return newItemSyncProgressLogger(repo, source, "issue", total)
}

func newMergeRequestSyncProgressLogger(repo RepoRef, source string, total int) itemSyncProgressLogger {
	return newItemSyncProgressLogger(repo, source, "merge request", total)
}

func newItemSyncProgressLogger(
	repo RepoRef,
	source string,
	item string,
	total int,
) itemSyncProgressLogger {
	progress := itemSyncProgressLogger{repo: repo, source: source, item: item, total: total}
	if progress.enabled() {
		progress.log(progress.item+" sync started", 0)
	}
	return progress
}

func (p itemSyncProgressLogger) record(processed int) {
	if !p.enabled() || processed >= p.total || processed%syncProgressLogInterval != 0 {
		return
	}
	p.log(p.item+" sync progress", processed)
}

func (p itemSyncProgressLogger) done() {
	if p.enabled() {
		p.log(p.item+" sync completed", p.total)
	}
}

func (p itemSyncProgressLogger) enabled() bool {
	return p.total >= syncProgressLogInterval
}

func (p itemSyncProgressLogger) log(message string, processed int) {
	slog.Info(message,
		"repo", p.repo.Owner+"/"+p.repo.Name,
		"platform", string(repoPlatform(p.repo)),
		"host", repoHost(p.repo),
		"source", p.source,
		"processed", processed,
		"total", p.total,
	)
}

func newIssueListFetchProgressLogger(repo RepoRef, source string) *listFetchProgressLogger {
	return newListFetchProgressLogger(repo, source, "issue")
}

func newMergeRequestListFetchProgressLogger(repo RepoRef, source string) *listFetchProgressLogger {
	return newListFetchProgressLogger(repo, source, "merge request")
}

func newListFetchProgressLogger(repo RepoRef, source, item string) *listFetchProgressLogger {
	return &listFetchProgressLogger{repo: repo, source: source, item: item}
}

func (p *listFetchProgressLogger) setTotal(total int) {
	if p != nil && total > 0 {
		p.total = total
	}
}

func (p *listFetchProgressLogger) recordPage(fetched int, hasMore bool) {
	if p == nil || fetched <= 0 {
		return
	}
	p.fetched += fetched
	if !p.started {
		if !hasMore && p.fetched < syncProgressLogInterval {
			return
		}
		p.started = true
		p.log(p.item + " list fetch started")
		return
	}
	if hasMore {
		p.log(p.item + " list fetch progress")
	}
}

func (p *listFetchProgressLogger) done() {
	if p != nil && p.started {
		if p.total == 0 {
			p.total = p.fetched
		}
		p.log(p.item + " list fetch completed")
	}
}

func (p *listFetchProgressLogger) log(message string) {
	attrs := []any{
		"repo", p.repo.Owner + "/" + p.repo.Name,
		"platform", string(repoPlatform(p.repo)),
		"host", repoHost(p.repo),
		"source", p.source,
		"fetched", p.fetched,
	}
	if p.total > 0 {
		attrs = append(attrs, "total", p.total)
	}
	slog.Info(message, attrs...)
}
