#!/usr/bin/env python3
"""Merge Forge provider history into a new database using only Python's stdlib.

Usage: python3 scripts/copy-provider-history.py source.db destination.db merged.db

Use SQLite backups from instances running the same schema. Neither input is
modified. Existing destination records win; completed source archive work fills
pending destination work. Copies PRs, issues, events, labels, review threads,
drafts, stars, workflow state and archive progress. Destination workspaces,
settings, credentials and runtime state stay in place. This does not enroll a
spoke or change configuration. Stop the destination daemon before installing the
result, keep its original database backup, and account for SQLite WAL files.
"""

import argparse
import contextlib
import os
from pathlib import Path
import sqlite3


def read_only(path):
    connection = sqlite3.connect(Path(path).resolve().as_uri() + '?mode=ro', uri=True)
    connection.row_factory = sqlite3.Row
    return connection


def merge(source, target):
    maps = {}
    inserted = {}

    def copy(table, keys, foreign=None, transform=None):
        foreign = foreign or {}
        mapping = maps[table] = {}
        added = inserted[table] = set()
        count = 0
        for original in source.execute(f'SELECT * FROM {table}'):
            row = dict(original)
            old_id = row.pop('id', None)
            for column, parent in foreign.items():
                row[column] = maps[parent][row[column]]
            if transform and not transform(row, original):
                continue
            where = ' AND '.join(f'{key} IS ?' for key in keys)
            values = [row[key] for key in keys]
            existing = target.execute(f'SELECT * FROM {table} WHERE {where}', values).fetchone()
            if existing is None:
                columns = ','.join(row)
                placeholders = ','.join('?' for _ in row)
                target.execute(f'INSERT INTO {table} ({columns}) VALUES ({placeholders})', list(row.values()))
                existing = target.execute(f'SELECT * FROM {table} WHERE {where}', values).fetchone()
                added.add(old_id)
                count += 1
            if old_id is not None:
                mapping[old_id] = existing['id']
        print(f'{table}: {count} added', flush=True)

    if source.execute("SELECT 1 FROM forge_repos WHERE platform_repo_id = '' LIMIT 1").fetchone():
        raise ValueError('Source has repositories without stable provider IDs')
    copy('forge_repos', ['platform', 'platform_host', 'platform_repo_id'])
    repo = {'repo_id': 'forge_repos'}

    def route(row, original):
        if original['repo_id'] not in inserted['forge_repos']:
            return False
        if row['is_current'] and target.execute(
            'SELECT 1 FROM forge_repo_routes WHERE platform=? AND platform_host=? '
            'AND repo_path_key=? AND is_current=1',
            (row['platform'], row['platform_host'], row['repo_path_key']),
        ).fetchone():
            row['is_current'] = 0
        return True

    copy('forge_repo_routes', ['repo_id', 'platform', 'platform_host', 'repo_path_key'], repo, route)
    for old_id in inserted['forge_repos']:
        target.execute("UPDATE forge_repos SET lifecycle_state='inactive' WHERE id=? "
                       'AND NOT EXISTS(SELECT 1 FROM forge_repo_routes WHERE repo_id=? AND is_current=1)',
                       (maps['forge_repos'][old_id], maps['forge_repos'][old_id]))
    for table in ('forge_merge_requests', 'forge_issues'):
        copy(table, ['repo_id', 'number'], repo)
    mr = {'merge_request_id': 'forge_merge_requests'}
    issue = {'issue_id': 'forge_issues'}
    copy('forge_mr_events', ['merge_request_id', 'dedupe_key'], mr)
    copy('forge_issue_events', ['issue_id', 'dedupe_key'], issue)
    copy('forge_mr_review_threads', ['merge_request_id', 'provider_thread_id'], mr)

    # A renamed label can retain its provider ID while its name changes.
    def label(row, original):
        existing = target.execute(
            "SELECT * FROM forge_labels WHERE repo_id=? AND ((platform_external_id<>'' "
            'AND platform_external_id=?) OR platform_id=? OR name=?) ORDER BY id LIMIT 1',
            (row['repo_id'], row['platform_external_id'], row['platform_id'], row['name']),
        ).fetchone()
        if existing:
            row['name'] = existing['name']
        return True

    copy('forge_labels', ['repo_id', 'name'], repo, label)
    for table, parent, foreign in (
        ('forge_merge_request_labels', 'merge_request_id', mr),
        ('forge_issue_labels', 'issue_id', issue),
    ):
        copy(table, [parent, 'label_id'], {**foreign, 'label_id': 'forge_labels'},
             lambda row, original: original[parent] in inserted[foreign[parent]])
    copy('forge_issue_pr_references', ['issue_id', 'source_provider', 'source_platform_host',
                                     'source_owner', 'source_repo', 'source_number'], issue)
    copy('forge_mr_review_drafts', ['merge_request_id'], mr)
    copy('forge_mr_review_draft_comments', ['draft_id', 'body', 'path', 'side', 'line', 'created_at'],
         {'draft_id': 'forge_mr_review_drafts'},
         lambda row, original: original['draft_id'] in inserted['forge_mr_review_drafts'])
    copy('forge_starred_items', ['repo_id', 'item_type', 'number'], repo)
    copy('forge_item_workflow_state', ['repo_id', 'item_type', 'item_number'], repo)
    copy('forge_archive_repos', ['repo_id'], repo)
    copy('forge_archive_items', ['repo_id', 'item_type', 'item_number'], repo)

    def progress(row, original):
        if 'parent_revision' in row:
            table = 'forge_issues' if row['item_type'] == 'issue' else 'forge_merge_requests'
            parent = target.execute(f'SELECT snapshot_revision FROM {table} WHERE repo_id=? AND number=?',
                                    (row['repo_id'], row['item_number'])).fetchone()
            row['parent_revision'] = parent[0] if parent else 0
        if row['status'] == 'running':
            row['status'] = 'pending'
        return True

    for table, keys in (
        ('forge_archive_repo_scans', ['repo_id', 'scan']),
        ('forge_archive_dataset_progress', ['repo_id', 'item_type', 'item_number', 'dataset']),
    ):
        copy(table, keys, repo, progress)
        for original in source.execute(f"SELECT * FROM {table} WHERE status='complete'"):
            row = dict(original)
            row['repo_id'] = maps['forge_repos'][row['repo_id']]
            progress(row, original)
            assignments = [f'{key}=?' for key in row if key not in keys and key != 'scan_generation']
            values = [row[key] for key in row if key not in keys and key != 'scan_generation']
            assignments.append('scan_generation=MAX(scan_generation,?)')
            values.append(row['scan_generation'])
            where = ' AND '.join(f'{key}=?' for key in keys)
            target.execute(f"UPDATE {table} SET {','.join(assignments)} WHERE {where} "
                           "AND status IN ('pending','running','blocked','failed')",
                           values + [row[key] for key in keys])


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('source')
    parser.add_argument('destination')
    parser.add_argument('output', help='New file; must not already exist')
    args = parser.parse_args()
    with contextlib.closing(read_only(args.source)) as source, contextlib.closing(read_only(args.destination)) as destination:
        schema = "SELECT type,name,sql FROM sqlite_master WHERE sql IS NOT NULL ORDER BY type,name"
        if [tuple(r) for r in source.execute(schema)] != [tuple(r) for r in destination.execute(schema)]:
            parser.error('Database schemas differ; use backups from the same Forge version')
        if source.execute('SELECT version,dirty FROM schema_migrations').fetchall() != destination.execute('SELECT version,dirty FROM schema_migrations').fetchall():
            parser.error('Database migration versions differ')
        if source.execute('SELECT dirty FROM schema_migrations').fetchone()[0]:
            parser.error('Database migration is incomplete')
        fd = os.open(args.output, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
        os.close(fd)
        try:
            with contextlib.closing(sqlite3.connect(args.output)) as target:
                destination.backup(target)
                target.row_factory = sqlite3.Row
                target.execute('PRAGMA journal_mode=DELETE')
                target.execute('PRAGMA foreign_keys=ON')
                with target:
                    merge(source, target)
                    if target.execute('PRAGMA foreign_key_check').fetchone():
                        raise ValueError('Merged database has foreign key violations')
                    if target.execute('PRAGMA integrity_check').fetchone()[0] != 'ok':
                        raise ValueError('Merged database failed integrity check')
        except BaseException:
            Path(args.output).unlink()
            raise
    print(f'Merged database: {args.output}')


if __name__ == '__main__':
    main()
