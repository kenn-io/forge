import { createSubscriber } from "svelte/reactivity";

const subscribeVisibility = createSubscriber((update) => {
  document.addEventListener("visibilitychange", update);
  return () => document.removeEventListener("visibilitychange", update);
});

export function documentVisible(): boolean {
  subscribeVisibility();
  return document.visibilityState !== "hidden";
}
