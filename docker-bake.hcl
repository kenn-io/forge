variable "GITLAB_VERSION" {
  default = "19.1.1-ce.0"
}

variable "GITLAB_BASE_IMAGE" {
  default = "gitlab/gitlab-ce:${GITLAB_VERSION}"
}

variable "GITLAB_FIXTURE_RUNTIME_IMAGE" {
  default = "middleman/gitlab-ce-fixture-runtime:${GITLAB_VERSION}"
}

variable "GITLAB_FIXTURE_IMAGE" {
  default = "middleman/gitlab-ce-fixture:${GITLAB_VERSION}"
}

group "default" {
  targets = ["gitlab-fixture-runtime"]
}

target "gitlab-fixture-runtime" {
  context = "scripts/e2e/gitlab"
  dockerfile = "Dockerfile.fixture"
  args = {
    GITLAB_BASE_IMAGE = "${GITLAB_BASE_IMAGE}"
  }
  tags = ["${GITLAB_FIXTURE_RUNTIME_IMAGE}"]
}

target "gitlab-fixture" {
  context = "scripts/e2e/gitlab"
  dockerfile = "Dockerfile.prebaked"
  args = {
    GITLAB_RUNTIME_IMAGE = "${GITLAB_FIXTURE_RUNTIME_IMAGE}"
  }
  tags = ["${GITLAB_FIXTURE_IMAGE}"]
}
