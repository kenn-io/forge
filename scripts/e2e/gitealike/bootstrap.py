#!/usr/bin/env python3
import base64
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request


def main():
    if len(sys.argv) != 2:
        print(f"usage: {sys.argv[0]} <manifest.json>", file=sys.stderr)
        return 2

    prefix = require_env("GITEALIKE_ENV_PREFIX")
    binary = require_env("GITEALIKE_BINARY")
    base_url = require_env(f"{prefix}_URL").rstrip("/")
    container_id = os.environ.get(f"{prefix}_CONTAINER_ID", "")
    title_prefix = os.environ.get(f"{prefix}_TITLE_PREFIX", prefix.title())
    manifest_path = sys.argv[1]
    fixture = Fixture(prefix, binary, base_url, container_id, title_prefix)
    manifest = fixture.bootstrap()
    with open(manifest_path, "w", encoding="utf-8") as f:
        json.dump(manifest, f, indent=2, sort_keys=True)
        f.write("\n")
    print(json.dumps(manifest, sort_keys=True))
    return 0


class Fixture:
    def __init__(self, prefix, binary, base_url, container_id, title_prefix):
        self.prefix = prefix
        self.binary = binary
        self.base_url = base_url
        self.api_url = f"{base_url}/api/v1"
        self.container_id = container_id
        self.title_prefix = title_prefix
        self.admin_user = "middleman-admin"
        self.admin_password = "middleman-admin-password"
        self.fixture_user = "middleman-user"
        self.fixture_password = "middleman-user-password"
        self.owner = "middleman-fixture"
        self.repo = "project-special"
        self.label = "middleman-fixture"
        self.branch = "feature/gitealike"
        self.status_context = "middleman/container-fixture"

    def bootstrap(self):
        self.wait_ready()
        self.ensure_admin_user()
        token = self.create_token()
        self.ensure_fixture_user(token)
        self.ensure_org(token)
        repo = self.ensure_repo(token)
        label = self.ensure_label(token)
        branch = self.ensure_branch(token)
        self.ensure_file(token, branch)
        branch = self.get_branch(token, self.branch)
        self.ensure_status(token, branch["commit"]["id"])
        pr = self.ensure_pull_request(token, label["id"])
        issue = self.ensure_issue(token, label["id"])
        self.ensure_comment(token, pr["number"], f"PR note from {self.title_prefix} container")
        self.ensure_comment(token, issue["number"], f"Issue note from {self.title_prefix} container")
        release = self.ensure_release(token)
        parsed = urllib.parse.urlparse(self.base_url)
        repo_path = f"{self.owner}/{self.repo}"
        return {
            "base_url": self.base_url,
            "api_url": self.api_url,
            "host": parsed.netloc,
            "token": token,
            "owner": self.owner,
            "name": self.repo,
            "repo_path": repo_path,
            "web_url": f"{self.base_url}/{repo_path}",
            "clone_url": f"{self.base_url}/{repo_path}.git",
            "default_branch": "main",
            "repository_id": repo["id"],
            "repository_id_string": str(repo["id"]),
            "pull_request_index": pr["number"],
            "issue_index": issue["number"],
            "label": label["name"],
            "release_tag": release["tag_name"],
            "status_context": self.status_context,
        }

    def wait_ready(self):
        deadline = time.monotonic() + int(os.environ.get(f"{self.prefix}_READY_TIMEOUT_SECONDS", "300"))
        last_error = None
        while time.monotonic() < deadline:
            try:
                version = self.request("GET", "/version", expected=(200,))
                if version:
                    return
            except Exception as exc:
                last_error = exc
                time.sleep(2)
        raise RuntimeError(f"{self.prefix} did not become ready at {self.base_url}: {last_error}")

    def ensure_admin_user(self):
        if not self.container_id:
            return
        cmd = [
            "docker",
            "exec",
            # Gitea and Forgejo refuse to run CLI commands as root, and
            # docker exec defaults to root; 1000:1000 matches USER_UID /
            # USER_GID in the compose fixtures.
            "--user",
            "1000:1000",
            self.container_id,
            self.binary,
            "admin",
            "user",
            "create",
            "--username",
            self.admin_user,
            "--password",
            self.admin_password,
            "--email",
            "middleman-admin@example.invalid",
            "--admin",
            "--must-change-password=false",
        ]
        result = subprocess.run(cmd, text=True, capture_output=True, check=False)
        output = result.stdout + result.stderr
        if result.returncode != 0 and "already exists" not in output.lower():
            raise RuntimeError(output)

    def create_token(self):
        name = f"middleman-e2e-{int(time.time())}"
        token = self.request(
            "POST",
            f"/users/{quote(self.admin_user)}/tokens",
            basic_auth=(self.admin_user, self.admin_password),
            data={"name": name, "scopes": ["all"]},
            expected=(200, 201),
        )
        value = token.get("sha1") or token.get("token")
        if not value:
            raise RuntimeError(f"token response did not include token material: {token}")
        return value

    def ensure_fixture_user(self, token):
        try:
            return self.request("GET", f"/users/{quote(self.fixture_user)}", token=token)
        except Exception:
            return self.request(
                "POST",
                "/admin/users",
                token=token,
                data={
                    "username": self.fixture_user,
                    "login_name": self.fixture_user,
                    "email": "middleman-user@example.invalid",
                    "password": self.fixture_password,
                    "must_change_password": False,
                    "source_id": 0,
                },
                expected=(201,),
            )

    def ensure_org(self, token):
        try:
            return self.request("GET", f"/orgs/{quote(self.owner)}", token=token)
        except Exception:
            return self.request(
                "POST",
                "/orgs",
                token=token,
                data={
                    "username": self.owner,
                    "full_name": "Middleman Fixture",
                    "description": "Middleman Gitea-compatible e2e fixture",
                },
                expected=(201,),
            )

    def ensure_repo(self, token):
        try:
            return self.request("GET", f"/repos/{quote(self.owner)}/{quote(self.repo)}", token=token)
        except Exception:
            return self.request(
                "POST",
                f"/orgs/{quote(self.owner)}/repos",
                token=token,
                data={
                    "name": self.repo,
                    "auto_init": True,
                    "default_branch": "main",
                    "private": False,
                    "description": "Middleman Gitea-compatible e2e fixture",
                },
                expected=(201,),
            )

    def ensure_label(self, token):
        labels = self.request("GET", self.repo_path("/labels"), token=token)
        for label in labels:
            if label.get("name") == self.label:
                return label
        return self.request(
            "POST",
            self.repo_path("/labels"),
            token=token,
            data={"name": self.label, "color": "#0052cc", "description": "Middleman container fixture"},
            expected=(201,),
        )

    def ensure_branch(self, token):
        try:
            return self.get_branch(token, self.branch)
        except Exception:
            self.request(
                "POST",
                self.repo_path("/branches"),
                token=token,
                data={"old_branch_name": "main", "new_branch_name": self.branch},
                expected=(201,),
            )
            return self.get_branch(token, self.branch)

    def get_branch(self, token, branch):
        return self.request("GET", self.repo_path(f"/branches/{quote(branch)}"), token=token)

    def ensure_file(self, token, branch):
        path = "src/gitealike-fixture.txt"
        content = f"{self.title_prefix} container fixture\n"
        encoded_path = quote(path)
        payload = {
            "branch": branch["name"],
            "content": base64.b64encode(content.encode()).decode(),
            "message": f"Add {self.title_prefix} fixture file",
        }
        try:
            existing = self.request(
                "GET",
                self.repo_path(f"/contents/{encoded_path}?ref={quote(branch['name'])}"),
                token=token,
            )
            existing_content = base64.b64decode("".join(existing.get("content", "").split())).decode()
            if existing_content == content:
                return
            payload["sha"] = existing["sha"]
            self.request("PUT", self.repo_path(f"/contents/{encoded_path}"), token=token, data=payload)
        except Exception as exc:
            if " returned 404:" not in str(exc):
                raise
            self.request("POST", self.repo_path(f"/contents/{encoded_path}"), token=token, data=payload, expected=(201,))

    def ensure_status(self, token, sha):
        statuses = self.request("GET", self.repo_path(f"/statuses/{quote(sha)}"), token=token)
        for status in statuses:
            if status.get("context") == self.status_context and status.get("state") == "success":
                return status
        return self.request(
            "POST",
            self.repo_path(f"/statuses/{quote(sha)}"),
            token=token,
            data={
                "state": "success",
                "context": self.status_context,
                "description": "Container fixture status",
                "target_url": f"{self.base_url}/{self.owner}/{self.repo}/actions",
            },
            expected=(201,),
        )

    def ensure_pull_request(self, token, label_id):
        pulls = self.request("GET", self.repo_path("/pulls?state=open"), token=token)
        title = f"{self.prefix.lower()} container PR"
        for pull in pulls:
            if pull.get("title") == title:
                self.ensure_labels(token, pull["number"], [label_id])
                return pull
        pull = self.request(
            "POST",
            self.repo_path("/pulls"),
            token=token,
            data={
                "head": self.branch,
                "base": "main",
                "title": title,
                "body": "Pull request seeded by middleman e2e.",
            },
            expected=(201,),
        )
        self.ensure_labels(token, pull["number"], [label_id])
        return pull

    def ensure_issue(self, token, label_id):
        title = f"{self.prefix.lower()} container issue"
        issues = self.request("GET", self.repo_path(f"/issues?state=open&q={quote(title)}"), token=token)
        for issue in issues:
            if issue.get("title") == title and not issue.get("pull_request"):
                self.ensure_labels(token, issue["number"], [label_id])
                return issue
        return self.request(
            "POST",
            self.repo_path("/issues"),
            token=token,
            data={
                "title": title,
                "body": "Issue seeded by middleman e2e.",
                "labels": [label_id],
            },
            expected=(201,),
        )

    def ensure_labels(self, token, number, label_ids):
        self.request(
            "POST",
            self.repo_path(f"/issues/{number}/labels"),
            token=token,
            data={"labels": label_ids},
            expected=(200, 201),
        )

    def ensure_comment(self, token, number, body):
        comments = self.request("GET", self.repo_path(f"/issues/{number}/comments"), token=token)
        for comment in comments:
            if comment.get("body") == body:
                return comment
        return self.request(
            "POST",
            self.repo_path(f"/issues/{number}/comments"),
            token=token,
            data={"body": body},
            expected=(201,),
        )

    def ensure_release(self, token):
        tag = "v1.0.0"
        releases = self.request("GET", self.repo_path("/releases"), token=token)
        for release in releases:
            if release.get("tag_name") == tag:
                return release
        return self.request(
            "POST",
            self.repo_path("/releases"),
            token=token,
            data={
                "tag_name": tag,
                "target_commitish": "main",
                "name": "Version 1.0.0",
                "body": "Release seeded by middleman e2e.",
            },
            expected=(201,),
        )

    def repo_path(self, suffix):
        return f"/repos/{quote(self.owner)}/{quote(self.repo)}{suffix}"

    def request(self, method, path, token=None, basic_auth=None, data=None, expected=(200,)):
        url = path if path.startswith("http") else f"{self.api_url}{path}"
        headers = {}
        body = None
        if token:
            headers["Authorization"] = f"token {token}"
        if basic_auth:
            raw = f"{basic_auth[0]}:{basic_auth[1]}".encode()
            headers["Authorization"] = "Basic " + base64.b64encode(raw).decode()
        if data is not None:
            body = json.dumps(data).encode()
            headers["Content-Type"] = "application/json"
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


def quote(value):
    return urllib.parse.quote(str(value), safe="")


def require_env(name):
    value = os.environ.get(name)
    if not value:
        raise RuntimeError(f"{name} is required")
    return value


if __name__ == "__main__":
    raise SystemExit(main())
