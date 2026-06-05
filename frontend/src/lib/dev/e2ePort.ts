import net from "node:net";

export function parseE2EPort(value: string | undefined): number | null {
  if (!value) return null;
  const parsed = Number(value);
  if (!Number.isInteger(parsed) || parsed < 1 || parsed > 65535) {
    return null;
  }
  return parsed;
}

export function e2eReuseExistingServer(env: Record<string, string | undefined> = process.env): boolean {
  switch (env.PLAYWRIGHT_REUSE_EXISTING_SERVER?.trim().toLowerCase()) {
    case "1":
    case "true":
    case "yes":
    case "on":
      return true;
    default:
      return false;
  }
}

export async function getAvailablePort(host = "127.0.0.1"): Promise<number> {
  return await new Promise((resolve, reject) => {
    const server = net.createServer();
    server.unref();
    server.on("error", reject);
    server.listen(0, host, () => {
      const address = server.address();
      if (address == null || typeof address === "string") {
        server.close(() => reject(new Error("failed to allocate test port")));
        return;
      }
      const { port } = address;
      server.close((err) => {
        if (err) {
          reject(err);
          return;
        }
        resolve(port);
      });
    });
  });
}
