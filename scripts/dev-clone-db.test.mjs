import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { mkdir, mkdtemp, readFile, realpath, stat, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);

async function sqlite(dbPath, sql) {
  const script = [
    "import sqlite3, sys",
    "db, sql = sys.argv[1], sys.argv[2]",
    "conn = sqlite3.connect(db)",
    "conn.executescript(sql)",
    "conn.commit()",
    "conn.close()",
  ].join("\n");
  await execFileAsync("python3", ["-c", script, dbPath, sql]);
}

test("dev-clone-db copies source database and rewrites cloned config", async () => {
  const tmp = await mkdtemp(path.join(os.tmpdir(), "middleman-dev-clone-test-"));
  const sourceHome = path.join(tmp, "source");
  const cloneDir = await realpath(path.join(tmp, "clone", ".."));
  const resolvedCloneDir = path.join(cloneDir, "clone");
  await mkdir(sourceHome, { recursive: true });
  await writeFile(
    path.join(sourceHome, "config.toml"),
    'host = "127.0.0.1"\nport = 8091\ndata_dir = "' + sourceHome + '"\n',
  );
  await sqlite(
    path.join(sourceHome, "middleman.db"),
    "CREATE TABLE sample (value TEXT); INSERT INTO sample VALUES ('copied');",
  );

  const { stdout } = await execFileAsync("bash", ["scripts/dev-clone-db.sh"], {
    env: {
      ...process.env,
      MIDDLEMAN_CONFIG: path.join(sourceHome, "config.toml"),
      MIDDLEMAN_DEV_CLONE_DIR: resolvedCloneDir,
      MIDDLEMAN_DEV_CLONE_PORT: "8123",
    },
  });

  assert.equal(stdout.trim(), path.join(resolvedCloneDir, "config.toml"));
  const clonedConfigPath = path.join(resolvedCloneDir, "config.toml");
  const clonedConfig = await readFile(clonedConfigPath, "utf8");
  assert.equal(clonedConfig.match(/^data_dir\s*=/gm)?.length, 1);
  assert.equal(clonedConfig.match(/^port\s*=/gm)?.length, 1);
  const parseConfigScript = [
    "import json, sys, tomllib",
    "with open(sys.argv[1], 'rb') as f:",
    "    config = tomllib.load(f)",
    "print(json.dumps({'data_dir': config.get('data_dir'), 'port': config.get('port')}))",
  ].join("\n");
  const { stdout: parsedConfigStdout } = await execFileAsync("python3", ["-c", parseConfigScript, clonedConfigPath]);
  assert.deepEqual(JSON.parse(parsedConfigStdout), { data_dir: resolvedCloneDir, port: 8123 });

  const queryScript = [
    "import sqlite3, sys",
    "conn = sqlite3.connect(sys.argv[1])",
    "print(conn.execute('SELECT value FROM sample').fetchone()[0])",
  ].join("\n");
  const { stdout: queryStdout } = await execFileAsync("python3", ["-c", queryScript, path.join(resolvedCloneDir, "middleman.db")]);
  assert.equal(queryStdout.trim(), "copied");

  const cloneDirMode = (await stat(resolvedCloneDir)).mode & 0o777;
  const cloneDBMode = (await stat(path.join(resolvedCloneDir, "middleman.db"))).mode & 0o777;
  const cloneConfigMode = (await stat(clonedConfigPath)).mode & 0o777;
  assert.equal(cloneDirMode, 0o700);
  assert.equal(cloneDBMode, 0o600);
  assert.equal(cloneConfigMode, 0o600);
});
