# Forge mobile

An Expo app with Forge’s web UI bundled into the APK. The selected Forge server
provides APIs, event streams, and WebSockets. Phones open the mobile workspace UI,
including ACP conversations and the terminal. Tablets start in the same mobile
view and offer **Use desktop layout** on the connection screen.

Enter your server's full URL, including its base path if configured, and an
optional Forge auth token. Leave the token blank when the server does not require
one or authenticates you through Tailscale Serve. Connect the device to your
network or tailnet before opening Forge. Both HTTP and HTTPS servers work;
HTTPS must have a certificate trusted by the device.

The app remembers one connection in Expo SecureStore. It uses Forge's existing
`auth_token` cookie bootstrap so API requests, event streams, and WebSockets
share the WebView session. Connected pages use Forge's own header. On Android,
Back navigates through page history, then returns to connection settings.
Use **Menu → Connection settings** on mobile, or **Connection settings** in the
desktop Settings page, to change servers or credentials on either platform.
Starting the app again also opens the saved connection form.
**Forget connection** removes the saved URL and token. WebView cookies and
browser storage follow the platform WebView’s private-browsing behavior. Links outside the server URL, including other paths on the same host, open in the browser.

## Run on Android

Install Node 24+, Bun, JDK 21, and Android Studio with the Android SDK and an
Android 16 / API 36 emulator. Set `ANDROID_HOME` to your SDK directory and
`JAVA_HOME` to your JDK directory, and put the SDK's `platform-tools` and
`emulator` directories on `PATH`. Linux emulators need access to `/dev/kvm`.

Install the root workspace dependencies, then build from this directory:

```sh
(cd .. && bun install --frozen-lockfile)
bun install --frozen-lockfile
node scripts/build-web.mjs
node node_modules/expo/bin/cli run:android
```

Select or start a virtual device in Android Studio's Device Manager first. The
command builds and installs the native app and starts Metro. No Expo account or
cloud build is required. Generated `android/` and `ios/` projects stay untracked.

For a Forge server on the development computer, forward its port to the emulator:

```sh
adb reverse tcp:8080 tcp:8080
```

Then enter `http://127.0.0.1:8080` in the app. Replace `8080` with your server's
actual port. This also preserves the loopback Host header Forge expects. For a
remote server, enter its normal URL and port instead.

After the first native build, start Metro with:

```sh
node node_modules/expo/bin/cli start --localhost
```

Rebuild the web bundle with `node scripts/build-web.mjs` after changing the
frontend. Rebuild the native app after changing dependencies or `app.json`.

For a standalone release APK with the current Forge web UI bundled, run:

```sh
node scripts/release-android.mjs
```

It rebuilds the web bundle, generates `android/` if missing, forces Gradle to
repackage the bundle, and prints the APK path. Add `--dry-run` to print the
commands without running them. The generated project signs release builds with
the debug keystore; distribution needs your own signing configuration.

Set `FORGE_MOBILE_VERSION` to a release such as `v0.4.0` to stamp that version
into the app. Its Android version code is `MAJOR * 1000000 + MINOR * 1000 + PATCH`,
so every later release installs as an update. Delete `android/` first, because
the version is applied only when the native project is generated.

## iOS

The Expo config includes iOS settings, but the iOS app has not been built or
verified yet. Android is the supported platform for now. To experiment on macOS
with Xcode, run `node scripts/build-web.mjs` followed by
`node node_modules/expo/bin/cli run:ios`.

## Checks

```sh
node node_modules/typescript/bin/tsc --noEmit
node --experimental-strip-types --test *.test.ts
node ../node_modules/vite-plus/bin/vp test run --config vitest.config.mts
node node_modules/expo/bin/cli export --platform android
```

UI updates require a new app build. Use a Forge server with APIs compatible with
the bundled frontend. The app does not run a Forge daemon on the phone or provide
offline workspaces.
