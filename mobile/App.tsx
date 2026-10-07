import { useEffect, useRef, useState } from "react";
import {
  ActivityIndicator,
  Alert,
  BackHandler,
  KeyboardAvoidingView,
  Linking,
  Platform,
  Pressable,
  ScrollView,
  StyleSheet,
  Switch,
  Text,
  TextInput,
  useColorScheme,
  useWindowDimensions,
  View,
} from "react-native";
import * as SecureStore from "expo-secure-store";
import { StatusBar } from "expo-status-bar";
import { SafeAreaProvider, SafeAreaView } from "react-native-safe-area-context";
import { WebView } from "react-native-webview";
import { isServerURL, launchURL, readConnection, serverURL, type Connection } from "./connection";

const storageKey = "forge.connection";
const emptyConnection: Connection = { server: "", token: "", desktop: false };
// Match Forge's quiet palette; native controls follow the OS color scheme.
const palettes = {
  light: {
    background: "#bfccdc",
    surface: "#cdd8e6",
    text: "#212a38",
    muted: "#4a5362",
    border: "#9fadc1",
    accent: "#245c9f",
    onAccent: "#ffffff",
    error: "#9f2536",
  },
  dark: {
    background: "#0f1012",
    surface: "#141518",
    text: "#e6edf3",
    muted: "#a0a9b5",
    border: "#33363c",
    accent: "#8cbcff",
    onAccent: "#0f1012",
    error: "#ff9da7",
  },
};

export default function App() {
  return (
    <SafeAreaProvider>
      <ForgeApp />
    </SafeAreaProvider>
  );
}

function ForgeApp() {
  const dark = useColorScheme() === "dark";
  const colors = palettes[dark ? "dark" : "light"];
  const { width, height } = useWindowDimensions();
  const tablet = Math.min(width, height) >= 600;
  const [draft, setDraft] = useState<Connection>(emptyConnection);
  const [connection, setConnection] = useState<Connection | null>(null);
  const [source, setSource] = useState<{ uri: string } | null>(null);
  const [ready, setReady] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [pageError, setPageError] = useState("");
  const [loading, setLoading] = useState(true);
  const [canGoBack, setCanGoBack] = useState(false);
  const webview = useRef<WebView>(null);

  useEffect(() => {
    let mounted = true;
    async function restore() {
      try {
        const saved = await SecureStore.getItemAsync(storageKey);
        if (saved && mounted) setDraft(readConnection(saved));
      } catch {
        if (mounted) setError("Could not read the saved connection. Enter your server again.");
      } finally {
        if (mounted) setReady(true);
      }
    }
    void restore();
    return () => {
      mounted = false;
    };
  }, []);

  useEffect(() => {
    const subscription = BackHandler.addEventListener("hardwareBackPress", () => {
      if (!connection) return false;
      if (canGoBack && !pageError) webview.current?.goBack();
      else disconnect();
      return true;
    });
    return () => subscription.remove();
  }, [connection, canGoBack, pageError]);

  function disconnect() {
    setConnection(null);
    setSource(null);
    setCanGoBack(false);
    setPageError("");
  }

  async function connect() {
    setError("");
    setSaving(true);
    try {
      const next = { ...draft, server: serverURL(draft.server), token: draft.token.trim() };
      const uri = launchURL(next, tablet);
      await SecureStore.setItemAsync(storageKey, JSON.stringify(next));
      setDraft(next);
      setPageError("");
      setLoading(true);
      setCanGoBack(false);
      setSource({ uri });
      setConnection(next);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Could not save the connection. Try again.");
    } finally {
      setSaving(false);
    }
  }

  async function forget() {
    try {
      await SecureStore.deleteItemAsync(storageKey);
      setDraft(emptyConnection);
      setError("");
    } catch {
      setError("Could not remove the saved connection. Try again.");
    }
  }

  function openExternal(target: string) {
    // Only web links leave the app. The bootstrap token never goes to another app.
    try {
      const url = new URL(target);
      if (!["https:", "http:"].includes(url.protocol) || url.searchParams.has("auth_token")) return;
      void Linking.openURL(url.toString()).catch(() =>
        Alert.alert("Could not open link", "Try again from your browser."),
      );
    } catch {
      /* A malformed navigation is not a web link. */
    }
  }

  const button = (label: string, action: () => void, primary = false, disabled = false) => (
    <Pressable
      accessibilityRole="button"
      disabled={disabled}
      onPress={action}
      style={({ pressed }) => [
        styles.button,
        {
          backgroundColor: primary ? colors.accent : colors.surface,
          borderColor: colors.border,
          opacity: pressed || disabled ? 0.6 : 1,
        },
      ]}
    >
      <Text style={[styles.buttonText, { color: primary ? colors.onAccent : colors.text }]}>{label}</Text>
    </Pressable>
  );

  return (
    <SafeAreaView style={[styles.root, { backgroundColor: colors.background }]}>
      <StatusBar style={dark ? "light" : "dark"} />
      {!ready ? (
        <ActivityIndicator style={styles.center} color={colors.accent} accessibilityLabel="Loading connection" />
      ) : connection && source ? (
        <>
          <View style={[styles.toolbar, { borderColor: colors.border }]}>
            <Text numberOfLines={1} style={[styles.server, { color: colors.muted }]}>
              {new URL(connection.server).host}
            </Text>
            {loading && !pageError && <ActivityIndicator color={colors.accent} accessibilityLabel="Connecting" />}
            {button("Server", disconnect)}
          </View>
          {pageError ? (
            <View style={styles.failure}>
              <Text accessibilityRole="header" style={[styles.heading, { color: colors.text }]}>
                Could not open Forge
              </Text>
              <Text accessibilityRole="alert" style={[styles.body, { color: colors.muted }]}>
                {pageError}
              </Text>
              {button(
                "Retry",
                () => {
                  setPageError("");
                  setLoading(true);
                  setSource({ uri: launchURL(connection, tablet) });
                },
                true,
              )}
              {button("Edit connection", disconnect)}
            </View>
          ) : (
            <WebView
              ref={webview}
              source={source}
              style={{ backgroundColor: colors.background }}
              incognito
              allowsBackForwardNavigationGestures
              contentMode={connection.desktop && tablet ? "desktop" : "mobile"}
              onLoadEnd={() => setLoading(false)}
              onNavigationStateChange={(state) => {
                setCanGoBack(state.canGoBack);
                setLoading(state.loading);
              }}
              onShouldStartLoadWithRequest={(request) => {
                if (isServerURL(request.url, connection.server)) return true;
                openExternal(request.url);
                return false;
              }}
              onOpenWindow={(event) => openExternal(event.nativeEvent.targetUrl)}
              onError={() =>
                setPageError("Check the server address and your network or Tailscale connection, then retry.")
              }
              onHttpError={(event) => {
                if (event.nativeEvent.statusCode === 401 || event.nativeEvent.statusCode === 403) {
                  setPageError(
                    "The server refused this connection. Check your auth token and the server's access settings.",
                  );
                } else if (event.nativeEvent.url === source.uri) {
                  setPageError(`The server returned HTTP ${event.nativeEvent.statusCode}. Try again.`);
                }
              }}
              onRenderProcessGone={() => setPageError("The web view stopped. Retry to reconnect to Forge.")}
              onContentProcessDidTerminate={() => setPageError("The web view stopped. Retry to reconnect to Forge.")}
            />
          )}
        </>
      ) : (
        <KeyboardAvoidingView style={styles.root} behavior={Platform.OS === "ios" ? "padding" : undefined}>
          <ScrollView contentContainerStyle={styles.setup} keyboardShouldPersistTaps="handled">
            <View style={styles.form}>
              <Text accessibilityRole="header" style={[styles.heading, { color: colors.text }]}>
                Connect to Forge
              </Text>
              <Text style={[styles.body, { color: colors.muted }]}>Your workspaces and agents, on your server.</Text>
              <View style={styles.field}>
                <Text style={[styles.label, { color: colors.text }]}>Server URL</Text>
                <TextInput
                  accessibilityLabel="Server URL"
                  value={draft.server}
                  onChangeText={(server) => setDraft({ ...draft, server })}
                  placeholder="https://forge.example.ts.net"
                  placeholderTextColor={colors.muted}
                  keyboardType="url"
                  autoCapitalize="none"
                  autoCorrect={false}
                  autoComplete="off"
                  style={[
                    styles.input,
                    { color: colors.text, backgroundColor: colors.surface, borderColor: colors.border },
                  ]}
                />
                <Text style={[styles.hint, { color: colors.muted }]}>
                  Use the address you open in a browser. Connect to Tailscale first if your server needs it.
                </Text>
              </View>
              <View style={styles.field}>
                <Text style={[styles.label, { color: colors.text }]}>Auth token (optional)</Text>
                <TextInput
                  accessibilityLabel="Auth token (optional)"
                  value={draft.token}
                  onChangeText={(token) => setDraft({ ...draft, token })}
                  secureTextEntry
                  autoCapitalize="none"
                  autoCorrect={false}
                  autoComplete="off"
                  placeholder="Leave blank if not required"
                  placeholderTextColor={colors.muted}
                  style={[
                    styles.input,
                    { color: colors.text, backgroundColor: colors.surface, borderColor: colors.border },
                  ]}
                />
                <Text style={[styles.hint, { color: colors.muted }]}>The connection is saved on this device.</Text>
              </View>
              {tablet && (
                <View style={styles.toggle}>
                  <Text style={[styles.label, styles.root, { color: colors.text }]}>Use desktop layout</Text>
                  <Switch
                    accessibilityLabel="Use desktop layout"
                    style={styles.switch}
                    value={draft.desktop}
                    onValueChange={(desktop) => setDraft({ ...draft, desktop })}
                  />
                </View>
              )}
              {!!error && (
                <Text accessibilityRole="alert" style={[styles.body, { color: colors.error }]}>
                  {error}
                </Text>
              )}
              {button(
                saving ? "Connecting…" : "Connect",
                () => {
                  void connect();
                },
                true,
                saving || !draft.server.trim(),
              )}
              {!!draft.server &&
                button(
                  "Forget connection",
                  () => {
                    void forget();
                  },
                  false,
                  saving,
                )}
            </View>
          </ScrollView>
        </KeyboardAvoidingView>
      )}
    </SafeAreaView>
  );
}

const styles = StyleSheet.create({
  root: { flex: 1 },
  center: { flex: 1, alignSelf: "center" },
  setup: { flexGrow: 1, padding: 24, justifyContent: "center" },
  form: { width: "100%", maxWidth: 480, alignSelf: "center", gap: 16 },
  heading: { fontSize: 28, lineHeight: 36, fontWeight: "600" },
  body: { fontSize: 16, lineHeight: 24 },
  label: { fontSize: 16, fontWeight: "500" },
  hint: { fontSize: 14, lineHeight: 20 },
  field: { gap: 8, marginTop: 8 },
  input: { borderWidth: 1, borderRadius: 8, minHeight: 52, paddingHorizontal: 16, paddingVertical: 12, fontSize: 16 },
  button: {
    minHeight: 48,
    paddingHorizontal: 16,
    paddingVertical: 12,
    borderRadius: 8,
    borderWidth: 1,
    alignItems: "center",
    justifyContent: "center",
  },
  buttonText: { fontSize: 16, fontWeight: "600" },
  toggle: { flexDirection: "row", alignItems: "center", gap: 16, minHeight: 48 },
  switch: { minWidth: 48, minHeight: 48 },
  toolbar: {
    flexDirection: "row",
    alignItems: "center",
    gap: 12,
    paddingHorizontal: 12,
    paddingVertical: 4,
    borderBottomWidth: 1,
  },
  server: { flex: 1, fontSize: 14 },
  failure: {
    flex: 1,
    justifyContent: "center",
    padding: 24,
    gap: 16,
    maxWidth: 520,
    width: "100%",
    alignSelf: "center",
  },
});
