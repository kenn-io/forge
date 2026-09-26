import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

const script = fileURLToPath(new URL("./cached-generate.sh", import.meta.url));

// The generator copies spec.txt into out.txt and appends a line to runs.log,
// so each test can count how many times generation actually ran.
function fixture(t) {
  const dir = mkdtempSync(join(tmpdir(), "cached-generate-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  spawnSync("git", ["init", "-q", dir]);
  writeFileSync(join(dir, "spec.txt"), "v1\n");
  const generate = (command = "cp spec.txt out.txt && echo run >> runs.log") =>
    spawnSync(script, ["example", "spec.txt", "out.txt", "--", "sh", "-c", command], {
      cwd: dir,
      encoding: "utf8",
    });
  const runs = () => readFileSync(join(dir, "runs.log"), "utf8").split("\n").filter(Boolean).length;
  return { dir, generate, runs };
}

test("skips generation when inputs and outputs are unchanged", (t) => {
  const { generate, runs } = fixture(t);

  assert.equal(generate().status, 0);
  const second = generate();

  assert.equal(second.status, 0);
  assert.match(second.stdout, /skipping generation/);
  assert.equal(runs(), 1);
});

test("regenerates when an input changes", (t) => {
  const { dir, generate, runs } = fixture(t);

  generate();
  writeFileSync(join(dir, "spec.txt"), "v2\n");
  generate();

  assert.equal(runs(), 2);
  assert.equal(readFileSync(join(dir, "out.txt"), "utf8"), "v2\n");
});

test("regenerates when generated output is edited or deleted", (t) => {
  const { dir, generate, runs } = fixture(t);

  generate();
  writeFileSync(join(dir, "out.txt"), "hand edit\n");
  generate();
  rmSync(join(dir, "out.txt"));
  generate();

  assert.equal(runs(), 3);
  assert.equal(readFileSync(join(dir, "out.txt"), "utf8"), "v1\n");
});

test("does not record a failed generation", (t) => {
  const { generate, runs } = fixture(t);

  assert.notEqual(generate("echo run >> runs.log && exit 3").status, 0);
  generate();

  assert.equal(runs(), 2);
});
