import { act } from "react";
import { create, type ReactTestRenderer } from "react-test-renderer";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import * as SecureStore from "expo-secure-store";
import { WebView, type WebViewProps } from "react-native-webview";
import App from "./App";

const native = vi.hoisted(() => ({
  back: (): boolean | undefined => false,
  goBack: vi.fn(),
  injectJavaScript: vi.fn(),
}));

vi.mock("react-native", () => ({
  ActivityIndicator: "ActivityIndicator",
  Alert: { alert: vi.fn() },
  BackHandler: {
    addEventListener: (_event: string, handler: typeof native.back) => {
      native.back = handler;
      return { remove() {} };
    },
  },
  KeyboardAvoidingView: "KeyboardAvoidingView",
  Linking: { openURL: vi.fn() },
  Platform: { OS: "android" },
  Pressable: "Pressable",
  ScrollView: "ScrollView",
  StyleSheet: { create: (styles: unknown) => styles, absoluteFill: {} },
  Switch: "Switch",
  Text: "Text",
  TextInput: "TextInput",
  useColorScheme: () => "light",
  useWindowDimensions: () => ({ width: 800, height: 1280 }),
  View: "View",
}));
vi.mock("expo-secure-store", () => ({
  getItemAsync: vi.fn(),
  setItemAsync: vi.fn(),
  deleteItemAsync: vi.fn(),
}));
vi.mock("expo-status-bar", () => ({ StatusBar: "StatusBar" }));
vi.mock("react-native-safe-area-context", () => ({
  SafeAreaProvider: "SafeAreaProvider",
  SafeAreaView: "SafeAreaView",
}));
vi.mock("react-native-webview", () => ({ WebView: "WebView" }));
vi.mock("./web-bundle", () => ({ loadWebBundle: async () => "bundled-ui" }));

let app: ReactTestRenderer;

async function mount() {
  await act(async () => {
    app = create(<App />, { createNodeMock: () => native });
  });
}

async function press(label: string) {
  const text = app.root.findAll(
    (node) => node.props.children === label && node.parent?.props.accessibilityRole === "button",
  )[0]!;
  await act(async () => text.parent!.props.onPress());
}

function webProps(): WebViewProps {
  return app.root.findByType(WebView).props;
}

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.clearAllMocks();
  vi.mocked(SecureStore.getItemAsync).mockResolvedValue(
    JSON.stringify({
      server: "https://forge.example.test/forge",
      token: "",
      desktop: false,
    }),
  );
});

afterEach(async () => {
  await act(async () => app?.unmount());
  vi.unstubAllGlobals();
});

describe("native connection flow", () => {
  it.each(["invalid", "unreadable"])("allows connection entry when saved data is %s", async (failure) => {
    if (failure === "invalid") vi.mocked(SecureStore.getItemAsync).mockResolvedValue("invalid JSON");
    else vi.mocked(SecureStore.getItemAsync).mockRejectedValue(new Error("Store unavailable"));
    await mount();
    expect(app.root.findByProps({ accessibilityRole: "alert" }).props.children).toContain("Enter your server again");
    await act(async () =>
      app.root.findByProps({ accessibilityLabel: "Server URL" }).props.onChangeText("https://forge.example.test"),
    );
    await press("Connect");
    expect(app.root.findAllByType(WebView)).toHaveLength(1);
  });

  it("restores the form and uses hardware Back for history before returning to settings", async () => {
    await mount();
    expect(app.root.findByProps({ accessibilityLabel: "Server URL" }).props.value).toBe(
      "https://forge.example.test/forge",
    );
    expect(native.back()).toBe(false);
    await press("Connect");
    await act(async () =>
      webProps().onNavigationStateChange?.({
        url: "https://forge.example.test/forge/m/workspaces/local/workspace-a",
        title: "Workspace",
        loading: false,
        canGoBack: true,
        canGoForward: false,
        navigationType: "other",
        lockIdentifier: 0,
      }),
    );
    await act(async () => expect(native.back()).toBe(true));
    expect(native.goBack).toHaveBeenCalledOnce();
    expect(app.root.findAllByType(WebView)).toHaveLength(1);
    await act(async () =>
      webProps().onNavigationStateChange?.({
        url: "https://forge.example.test/forge/m/workspaces",
        title: "Workspaces",
        loading: false,
        canGoBack: false,
        canGoForward: false,
        navigationType: "other",
        lockIdentifier: 0,
      }),
    );
    await act(async () => expect(native.back()).toBe(true));
    expect(app.root.findByProps({ accessibilityLabel: "Server URL" }).props.value).toBe(
      "https://forge.example.test/forge",
    );
  });

  it("keeps the WebView and its history for a same-server full-page link", async () => {
    await mount();
    await press("Connect");
    const view = app.root.findByType(WebView);
    const source = webProps().source;
    await act(async () => {
      expect(
        webProps().onShouldStartLoadWithRequest?.({
          url: "https://forge.example.test/forge/",
          isTopFrame: true,
          title: "Forge",
          loading: false,
          canGoBack: true,
          canGoForward: false,
          navigationType: "click",
          lockIdentifier: 0,
        }),
      ).toBe(false);
    });
    expect(app.root.findByType(WebView) === view).toBe(true);
    expect(webProps().source).toBe(source);
    expect(native.injectJavaScript).toHaveBeenCalledOnce();
  });

  it("opens connection settings on a bridge message and ignores unknown messages", async () => {
    await mount();
    await press("Connect");
    for (const data of ["invalid JSON", '{"type":"unrelated"}']) {
      await act(async () => app.root.findByType(WebView).props.onMessage({ nativeEvent: { data } }));
      expect(app.root.findAllByType(WebView)).toHaveLength(1);
    }
    await act(async () =>
      app.root.findByType(WebView).props.onMessage({ nativeEvent: { data: '{"type":"connection-settings"}' } }),
    );
    expect(app.root.findByProps({ accessibilityLabel: "Server URL" }).props.value).toBe(
      "https://forge.example.test/forge",
    );
  });

  it("shows a connection error, retries the bundle, and returns to settings on Back", async () => {
    await mount();
    await press("Connect");
    await act(async () =>
      app.root.findByType(WebView).props.onMessage({
        nativeEvent: {
          data: JSON.stringify({ type: "connection-error", message: "Server refused connection" }),
        },
      }),
    );
    expect(app.root.findByProps({ accessibilityRole: "alert" }).props.children).toBe("Server refused connection");
    await press("Retry");
    expect(app.root.findAllByType(WebView)).toHaveLength(1);
    await act(async () => app.root.findByType(WebView).props.onError({}));
    expect(app.root.findByProps({ accessibilityRole: "alert" }).props.children).toContain("Check the server address");
    await act(async () => expect(native.back()).toBe(true));
    expect(app.root.findAllByType(WebView)).toHaveLength(0);
    expect(app.root.findByProps({ accessibilityLabel: "Server URL" }).props.value).toBe(
      "https://forge.example.test/forge",
    );
  });
});
