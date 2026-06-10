import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, test, vi } from "vite-plus/test";

import type { MessagesCapabilities } from "../../api/messages/types";
import MessagesSetupDialog from "./MessagesSetupDialog.svelte";

afterEach(cleanup);

const syntheticURL = "https://messages.example.com";
const syntheticEnv = "MSGVAULT_API_KEY";

const okCapabilities: MessagesCapabilities = {
  configured: true,
  ok: true,
  status: "ok",
  modes: ["fts"],
  features: {
    threads_endpoint: true,
    mutations: false,
    attachments_download: false,
    sse_events: false,
  },
};

interface RenderOpts {
  open?: boolean;
  initialURL?: string;
  initialEnv?: string;
  onSave?: (input: { url: string; api_key_env: string }) => Promise<MessagesCapabilities>;
  onClose?: () => void;
}

function renderDialog(opts: RenderOpts = {}) {
  const onSave = opts.onSave ?? vi.fn().mockResolvedValue(okCapabilities);
  const onClose = opts.onClose ?? vi.fn();
  const props = {
    open: opts.open ?? true,
    onSave,
    onClose,
    ...(opts.initialURL !== undefined ? { initialURL: opts.initialURL } : {}),
    ...(opts.initialEnv !== undefined ? { initialEnv: opts.initialEnv } : {}),
  };
  const result = render(MessagesSetupDialog, { props });
  return { ...result, onSave, onClose };
}

function getURLInput(): HTMLInputElement {
  return screen.getByLabelText(/Message source URL/i) as HTMLInputElement;
}

function getEnvInput(): HTMLInputElement {
  return screen.getByLabelText(/API key env var name/i) as HTMLInputElement;
}

function getSubmit(): HTMLButtonElement {
  return screen.getByRole("button", { name: /^save/i }) as HTMLButtonElement;
}

function getForm(): HTMLFormElement {
  return getSubmit().closest("form") as HTMLFormElement;
}

describe("MessagesSetupDialog structure", () => {
  test("renders both inputs when open", () => {
    renderDialog();

    expect(getURLInput()).toBeTruthy();
    expect(getEnvInput()).toBeTruthy();
  });

  test("renders nothing when closed", () => {
    renderDialog({ open: false });

    expect(screen.queryByLabelText(/Message source URL/i)).toBeNull();
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  test("cancel calls onClose", async () => {
    const { onClose } = renderDialog();

    await fireEvent.click(screen.getByRole("button", { name: /cancel/i }));

    expect(onClose).toHaveBeenCalledTimes(1);
  });

  test("Escape calls onClose", async () => {
    const { onClose } = renderDialog();

    await fireEvent.keyDown(document, { key: "Escape" });

    expect(onClose).toHaveBeenCalledTimes(1);
  });

  test("focuses the URL input when opened", async () => {
    renderDialog();

    await waitFor(() => {
      expect(document.activeElement).toBe(getURLInput());
    });
  });
});

describe("MessagesSetupDialog pre-fill", () => {
  test("pre-fills from initialURL and initialEnv", () => {
    renderDialog({
      initialURL: syntheticURL,
      initialEnv: syntheticEnv,
    });

    expect(getURLInput().value).toBe(syntheticURL);
    expect(getEnvInput().value).toBe(syntheticEnv);
  });

  test("env var input defaults to MSGVAULT_API_KEY when initialEnv is undefined", () => {
    renderDialog();

    expect(getEnvInput().value).toBe(syntheticEnv);
  });

  test("URL input is empty when initialURL is undefined", () => {
    renderDialog();

    expect(getURLInput().value).toBe("");
  });

  test("empty-string initialEnv is respected", () => {
    renderDialog({ open: true, initialEnv: "" });

    expect(getEnvInput().value).toBe("");
  });

  test("re-opening the dialog re-seeds from initial props", async () => {
    const onSave = vi.fn().mockResolvedValue(okCapabilities);
    const onClose = vi.fn();
    const baseProps = {
      open: true,
      initialURL: syntheticURL,
      initialEnv: syntheticEnv,
      onSave,
      onClose,
    };
    const { rerender } = render(MessagesSetupDialog, { props: baseProps });
    expect(getURLInput().value).toBe(syntheticURL);

    await fireEvent.input(getURLInput(), {
      target: { value: "https://typed.example.com" },
    });
    expect(getURLInput().value).toBe("https://typed.example.com");
    await rerender({ ...baseProps, open: false });

    await rerender({ ...baseProps, open: true });
    expect(getURLInput().value).toBe(syntheticURL);
  });
});

describe("MessagesSetupDialog client validation", () => {
  test("submit on empty URL is blocked by the JS validator", async () => {
    const { onSave } = renderDialog();

    await fireEvent.submit(getForm());

    expect(onSave).not.toHaveBeenCalled();
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toMatch(/URL must include a scheme and host/i);
  });

  test("submit with https:///foo is rejected", async () => {
    const { onSave } = renderDialog();

    await fireEvent.input(getURLInput(), { target: { value: "https:///foo" } });
    await fireEvent.submit(getForm());

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toMatch(/URL must include a scheme and host/i);
    expect(onSave).not.toHaveBeenCalled();
  });

  test("submit with single-slash https:/foo is rejected", async () => {
    const { onSave } = renderDialog();

    await fireEvent.input(getURLInput(), { target: { value: "https:/foo" } });
    await fireEvent.submit(getForm());

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toMatch(/URL must include a scheme and host/i);
    expect(onSave).not.toHaveBeenCalled();
  });

  test("submit with no-slash https:foo is rejected", async () => {
    const { onSave } = renderDialog();

    await fireEvent.input(getURLInput(), { target: { value: "https:foo" } });
    await fireEvent.submit(getForm());

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toMatch(/URL must include a scheme and host/i);
    expect(onSave).not.toHaveBeenCalled();
  });

  test("submit with malformed URL shows the scheme-and-host error", async () => {
    const { onSave } = renderDialog();

    await fireEvent.input(getURLInput(), { target: { value: "not a url" } });
    await fireEvent.submit(getForm());

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toMatch(/URL must include a scheme and host/i);
    expect(onSave).not.toHaveBeenCalled();
  });

  test("submit with non-http scheme shows the scheme-and-host error", async () => {
    const { onSave } = renderDialog();

    await fireEvent.input(getURLInput(), {
      target: { value: "ftp://messages.example.com" },
    });
    await fireEvent.submit(getForm());

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toMatch(/URL must include a scheme and host/i);
    expect(onSave).not.toHaveBeenCalled();
  });

  test("submit with plaintext http to a non-loopback host is rejected", async () => {
    const { onSave } = renderDialog();

    await fireEvent.input(getURLInput(), {
      target: { value: "http://messages.example.com" },
    });
    await fireEvent.submit(getForm());

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toMatch(/cleartext/i);
    expect(onSave).not.toHaveBeenCalled();
  });

  test("submit with plaintext http to localhost is accepted", async () => {
    const { onSave } = renderDialog();

    await fireEvent.input(getURLInput(), {
      target: { value: "http://localhost:7777" },
    });
    await fireEvent.submit(getForm());

    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));
    expect(onSave).toHaveBeenCalledWith({
      url: "http://localhost:7777",
      api_key_env: "MSGVAULT_API_KEY",
    });
  });

  test("lowercase env var name shows a client-side error", async () => {
    const { onSave } = renderDialog();

    await fireEvent.input(getURLInput(), { target: { value: syntheticURL } });
    await fireEvent.input(getEnvInput(), { target: { value: "msgvault_api_key" } });
    await fireEvent.submit(getForm());

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toMatch(/env var name/i);
    expect(onSave).not.toHaveBeenCalled();
  });

  test("env var name starting with a digit is rejected", async () => {
    const { onSave } = renderDialog();

    await fireEvent.input(getURLInput(), { target: { value: syntheticURL } });
    await fireEvent.input(getEnvInput(), { target: { value: "1MSGVAULT_API_KEY" } });
    await fireEvent.submit(getForm());

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toMatch(/env var name/i);
    expect(onSave).not.toHaveBeenCalled();
  });
});

describe("MessagesSetupDialog save flow", () => {
  test("submit with valid values calls onSave with url and api_key_env", async () => {
    const onSave = vi.fn().mockResolvedValue(okCapabilities);
    renderDialog({ onSave });

    await fireEvent.input(getURLInput(), { target: { value: syntheticURL } });
    await fireEvent.submit(getForm());

    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));
    expect(onSave).toHaveBeenCalledWith({
      url: syntheticURL,
      api_key_env: syntheticEnv,
    });
  });

  test("onSave resolution closes the dialog", async () => {
    const onSave = vi.fn().mockResolvedValue(okCapabilities);
    const onClose = vi.fn();
    renderDialog({ onSave, onClose });

    await fireEvent.input(getURLInput(), { target: { value: syntheticURL } });
    await fireEvent.submit(getForm());

    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
  });

  test("onSave rejection keeps the dialog open and shows the error", async () => {
    const onSave = vi.fn().mockRejectedValueOnce(new Error("server says no"));
    const onClose = vi.fn();
    renderDialog({ onSave, onClose });

    await fireEvent.input(getURLInput(), { target: { value: syntheticURL } });
    await fireEvent.submit(getForm());

    const alert = await waitFor(() => screen.getByRole("alert"));
    expect(alert.textContent).toContain("server says no");
    expect(onClose).not.toHaveBeenCalled();
    expect(getURLInput()).toBeTruthy();
  });

  test("trims whitespace before validating and dispatching", async () => {
    const onSave = vi.fn().mockResolvedValue(okCapabilities);
    renderDialog({ onSave });

    await fireEvent.input(getURLInput(), {
      target: { value: `  ${syntheticURL}  ` },
    });
    await fireEvent.input(getEnvInput(), {
      target: { value: `  ${syntheticEnv}  ` },
    });
    await fireEvent.submit(getForm());

    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));
    expect(onSave).toHaveBeenCalledWith({
      url: syntheticURL,
      api_key_env: syntheticEnv,
    });
  });

  test("does not re-submit while a save is in flight", async () => {
    let resolveSave: (caps: MessagesCapabilities) => void = () => {};
    const onSave = vi.fn(
      () =>
        new Promise<MessagesCapabilities>((resolve) => {
          resolveSave = resolve;
        }),
    );
    renderDialog({ onSave });

    await fireEvent.input(getURLInput(), { target: { value: syntheticURL } });
    await fireEvent.input(getEnvInput(), { target: { value: syntheticEnv } });

    const saveButton = getSubmit();
    const form = getForm();
    await fireEvent.click(saveButton);
    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));
    expect(saveButton.disabled).toBe(true);

    await fireEvent.submit(form);
    expect(onSave).toHaveBeenCalledTimes(1);

    resolveSave(okCapabilities);
  });
});
