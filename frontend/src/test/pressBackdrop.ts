import { fireEvent } from "@testing-library/svelte";

/** A full press on an overlay backdrop: kit-ui's backdropCloses closes on
 * the click of a press that starts and ends on the backdrop. */
export async function pressBackdrop(backdrop: HTMLElement): Promise<void> {
  await fireEvent.pointerDown(backdrop);
  await fireEvent.pointerUp(backdrop);
  await fireEvent.click(backdrop);
}
