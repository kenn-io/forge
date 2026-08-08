import { createContext } from "svelte";
import type { AppRuntime } from "./runtime.js";

export const [getAppRuntime, setAppRuntime] = createContext<AppRuntime>();
