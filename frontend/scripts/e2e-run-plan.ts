const defaultProjectRuns = [["--project=chromium"], ["--project=firefox"]];

function includesProjectArg(args: string[]): boolean {
  return args.some((arg) => arg === "--project" || arg.startsWith("--project="));
}

export function planE2ERuns(requestedArgs: string[]): string[][] {
  if (requestedArgs.length === 0) {
    return defaultProjectRuns;
  }
  if (includesProjectArg(requestedArgs)) {
    return [requestedArgs];
  }
  return defaultProjectRuns.map((projectArgs) => [...projectArgs, ...requestedArgs]);
}
