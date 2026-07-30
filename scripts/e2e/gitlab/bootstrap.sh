#!/usr/bin/env bash
set -euo pipefail

MANIFEST_PATH="${1:-${GITLAB_MANIFEST:-}}"
if [ -z "$MANIFEST_PATH" ]; then
  printf 'usage: %s <manifest.json>\n' "$0" >&2
  exit 2
fi

: "${GITLAB_URL:?GITLAB_URL is required}"
: "${GITLAB_ROOT_PASSWORD:?GITLAB_ROOT_PASSWORD is required}"

python3 - "$MANIFEST_PATH" <<'PY'
import base64
import json
import os
import sys
import time
from datetime import date, timedelta
import urllib.error
import urllib.parse
import urllib.request

manifest_path = sys.argv[1]
base_url = os.environ["GITLAB_URL"].rstrip("/")
root_password = os.environ["GITLAB_ROOT_PASSWORD"]
api_url = f"{base_url}/api/v4"


def request(method, path, token=None, data=None, form=None, expected=(200, 201)):
    url = path if path.startswith("http") else f"{api_url}{path}"
    headers = {}
    body = None
    if token:
        headers["Authorization"] = f"Bearer {token}"
    if data is not None:
        body = json.dumps(data).encode()
        headers["Content-Type"] = "application/json"
    if form is not None:
        body = urllib.parse.urlencode(form).encode()
        headers["Content-Type"] = "application/x-www-form-urlencoded"
    req = urllib.request.Request(url, data=body, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            raw = resp.read()
            if resp.status not in expected:
                raise RuntimeError(f"{method} {url} returned {resp.status}: {raw[:400]!r}")
            if not raw:
                return {}
            return json.loads(raw.decode())
    except urllib.error.HTTPError as exc:
        raw = exc.read()
        if exc.code in expected:
            if not raw:
                return {}
            return json.loads(raw.decode())
        raise RuntimeError(f"{method} {url} returned {exc.code}: {raw[:800]!r}") from exc


def wait_for_gitlab():
    deadline = time.monotonic() + int(os.environ.get("GITLAB_READY_TIMEOUT_SECONDS", "1200"))
    while time.monotonic() < deadline:
        try:
            req = urllib.request.Request(f"{base_url}/users/sign_in", method="HEAD")
            with urllib.request.urlopen(req, timeout=30) as resp:
                if resp.status == 200 and resp.headers.get("X-Gitlab-Meta"):
                    return
                raise RuntimeError(f"unexpected readiness response {resp.status}")
        except urllib.error.HTTPError as exc:
            if exc.code == 200 and exc.headers.get("X-Gitlab-Meta"):
                return
        except Exception:
            time.sleep(5)
            continue
        time.sleep(5)
    raise RuntimeError(f"GitLab did not become ready at {base_url}")


def root_token():
    deadline = time.monotonic() + 180
    last_error = None
    while time.monotonic() < deadline:
        try:
            token = request(
                "POST",
                f"{base_url}/oauth/token",
                form={
                    "grant_type": "password",
                    "username": "root",
                    "password": root_password,
                },
            )
            return token["access_token"]
        except Exception as exc:
            last_error = exc
            time.sleep(5)
    raise RuntimeError(f"could not obtain root OAuth token: {last_error}")


def create_personal_access_token(admin_token):
    token = request(
        "POST",
        "/users/1/personal_access_tokens",
        token=admin_token,
        form={
            "name": f"kenn-forge-e2e-{int(time.time())}",
            "scopes[]": "api",
            "expires_at": (date.today() + timedelta(days=30)).isoformat(),
        },
    )
    return token["token"]


def get_or_create_group(token, name, path, parent_id=None):
    encoded = urllib.parse.quote(path if parent_id is None else f"kenn-forge-fixture/{path}", safe="")
    try:
        return request("GET", f"/groups/{encoded}", token=token)
    except Exception:
        payload = {"name": name, "path": path, "visibility": "private"}
        if parent_id is not None:
            payload["parent_id"] = parent_id
        return request("POST", "/groups", token=token, data=payload)


def get_project(token, project_path):
    encoded = urllib.parse.quote(project_path, safe="")
    return request("GET", f"/projects/{encoded}", token=token)


def get_or_create_project(token, namespace_id, project_path):
    try:
        return get_project(token, project_path)
    except Exception:
        return request(
            "POST",
            "/projects",
            token=token,
            data={
                "name": "project-special",
                "path": "project-special",
                "namespace_id": namespace_id,
                "visibility": "private",
                "initialize_with_readme": True,
                "default_branch": "main",
            },
        )


def project_path(project):
    return urllib.parse.quote(str(project["id"]), safe="")


def ensure_label(token, project, name):
    pid = project_path(project)
    labels = request("GET", f"/projects/{pid}/labels", token=token)
    for label in labels:
        if label.get("name") == name:
            return label
    return request(
        "POST",
        f"/projects/{pid}/labels",
        token=token,
        data={"name": name, "color": "#0052cc", "description": "Kenn Forge GitLab e2e fixture"},
    )


def ensure_branch_and_file(token, project):
    pid = project_path(project)
    branches = request("GET", f"/projects/{pid}/repository/branches", token=token)
    if not any(branch.get("name") == "feature/gitlab" for branch in branches):
        request(
            "POST",
            f"/projects/{pid}/repository/branches",
            token=token,
            form={"branch": "feature/gitlab", "ref": "main"},
        )
    file_path = urllib.parse.quote("src/gitlab-fixture.txt", safe="")
    payload = {
        "branch": "feature/gitlab",
        "content": "GitLab container fixture\n",
        "commit_message": "Add GitLab fixture file",
    }
    try:
        existing = request("GET", f"/projects/{pid}/repository/files/{file_path}?ref=feature%2Fgitlab", token=token)
        encoded_content = "".join(existing.get("content", "").split())
        if base64.b64decode(encoded_content).decode() == payload["content"]:
            return
        request("PUT", f"/projects/{pid}/repository/files/{file_path}", token=token, data=payload)
    except Exception as exc:
        if " returned 404:" not in str(exc):
            raise
        request("POST", f"/projects/{pid}/repository/files/{file_path}", token=token, data=payload)


def ensure_merge_request(token, project, label):
    pid = project_path(project)
    existing = request(
        "GET",
        f"/projects/{pid}/merge_requests?state=opened&source_branch=feature%2Fgitlab&target_branch=main",
        token=token,
    )
    for mr in existing:
        if mr.get("title") == "GitLab container MR":
            return mr
    return request(
        "POST",
        f"/projects/{pid}/merge_requests",
        token=token,
        data={
            "source_branch": "feature/gitlab",
            "target_branch": "main",
            "title": "GitLab container MR",
            "description": "Merge request seeded by kenn-forge e2e.",
            "labels": label,
        },
    )


def ensure_issue(token, project, label):
    pid = project_path(project)
    existing = request("GET", f"/projects/{pid}/issues?search=GitLab%20container%20issue", token=token)
    for issue in existing:
        if issue.get("title") == "GitLab container issue":
            return issue
    return request(
        "POST",
        f"/projects/{pid}/issues",
        token=token,
        data={
            "title": "GitLab container issue",
            "description": "Issue seeded by kenn-forge e2e.",
            "labels": label,
        },
    )


def ensure_note(token, project, item, kind, body):
    pid = project_path(project)
    iid = item["iid"]
    notes_path = "merge_requests" if kind == "mr" else "issues"
    notes = request("GET", f"/projects/{pid}/{notes_path}/{iid}/notes", token=token)
    for note in notes:
        if note.get("body") == body:
            return note
    return request("POST", f"/projects/{pid}/{notes_path}/{iid}/notes", token=token, data={"body": body})


def ensure_tag_and_release(token, project):
    pid = project_path(project)
    tags = request("GET", f"/projects/{pid}/repository/tags", token=token)
    if not any(tag.get("name") == "v1.0.0" for tag in tags):
        request("POST", f"/projects/{pid}/repository/tags", token=token, form={"tag_name": "v1.0.0", "ref": "main"})
    releases = request("GET", f"/projects/{pid}/releases", token=token)
    for release in releases:
        if release.get("tag_name") == "v1.0.0":
            return release
    return request(
        "POST",
        f"/projects/{pid}/releases",
        token=token,
        data={
            "name": "Version 1.0.0",
            "tag_name": "v1.0.0",
            "description": "Release seeded by kenn-forge e2e.",
        },
    )


wait_for_gitlab()
token = root_token()
group = get_or_create_group(token, "kenn-forge-fixture", "kenn-forge-fixture")
subgroup = get_or_create_group(token, "nested", "nested", group["id"])
project = get_or_create_project(token, subgroup["id"], "kenn-forge-fixture/nested/project-special")
label = ensure_label(token, project, "kenn-forge-fixture")
ensure_branch_and_file(token, project)
mr = ensure_merge_request(token, project, label["name"])
issue = ensure_issue(token, project, label["name"])
mr_note = ensure_note(token, project, mr, "mr", "MR note from GitLab container")
issue_note = ensure_note(token, project, issue, "issue", "Issue note from GitLab container")
release = ensure_tag_and_release(token, project)
api_token = create_personal_access_token(token)

parsed = urllib.parse.urlparse(base_url)
manifest = {
    "base_url": base_url,
    "api_url": api_url,
    "host": parsed.netloc,
    "token": api_token,
    "owner": "kenn-forge-fixture/nested",
    "name": "project-special",
    "repo_path": "kenn-forge-fixture/nested/project-special",
    "web_url": f"{base_url}/kenn-forge-fixture/nested/project-special",
    "clone_url": f"{base_url}/kenn-forge-fixture/nested/project-special.git",
    "default_branch": "main",
    "project_id": project["id"],
    "project_external_id": f"gid://gitlab/Project/{project['id']}",
    "merge_request_iid": mr["iid"],
    "issue_iid": issue["iid"],
    "label": label["name"],
    "merge_request_note_id": mr_note["id"],
    "issue_note_id": issue_note["id"],
    "release_tag": release["tag_name"],
}
with open(manifest_path, "w", encoding="utf-8") as f:
    json.dump(manifest, f, indent=2, sort_keys=True)
    f.write("\n")
print(json.dumps(manifest, sort_keys=True))
PY
