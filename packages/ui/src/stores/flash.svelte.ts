// The flash store is kit-ui's: one module instance shared by every consumer
// that imports `@middleman/ui/stores/flash`, so keyboard, detail, and app-shell
// flashes all land in the single mounted kit FlashBanner. kit's store stacks
// (up to 5) and supports per-flash dismissal by id and semantic tones.
//
// Stacking is an intentional semantics change from the replaced local stores,
// which were single-message latest-wins: bursts of errors now stay visible
// together and each dismisses independently (or on its own timeout). Do not
// write callers or tests that assume a new flash replaces the previous one.
export {
  showFlash,
  dismissFlash,
  getFlash,
  getFlashes,
  getFlashMessage,
  type FlashOptions,
  type FlashState,
  type FlashTone,
} from "@kenn-io/kit-ui";
