# Forge mobile

An Expo app for an existing Forge server. Phones open the mobile workspace UI,
including ACP conversations and the terminal. Tablets start in the same mobile
view and offer **Use desktop layout** on the connection screen.

Enter your server's full URL, including its base path if configured, and an
optional Forge auth token. Leave the token blank when the server does not require
one or authenticates you through Tailscale Serve. Connect the device to your
network or tailnet before opening Forge. Both HTTP and HTTPS servers work;
HTTPS must have a certificate trusted by the device.

The app remembers one connection in Expo SecureStore. It uses Forge's existing
`auth_token` cookie bootstrap so API requests, event streams, and WebSockets
share the WebView session. **Server** returns to connection settings;
**Forget connection** removes the saved URL and token. WebView cookies and
browser storage are session-only. Links to other origins open in the browser.

## Run on Android

Install Node 24+, Bun, JDK 21, and Android Studio with the Android SDK and an
Android 16 / API 36 emulator. Set `ANDROID_HOME` to your SDK directory and
`JAVA_HOME` to your JDK directory, and put the SDK's `platform-tools` and
`emulator` directories on `PATH`. Linux emulators need access to `/dev/kvm`.

From this directory:

```sh
bun install --frozen-lockfile
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

Rebuild the native app after changing dependencies or `app.json`. A standalone
local Android build, with its JavaScript bundle included, is available through
`node node_modules/expo/bin/cli run:android --variant release`. Distribution still
needs your own signing configuration.

## iOS

On macOS with Xcode and an iOS simulator installed, run
`node node_modules/expo/bin/cli run:ios`. The native shell supports iPhone and
iPad. Android is the locally verified platform for this initial app.

## Checks

```sh
node node_modules/typescript/bin/tsc --noEmit
node --experimental-strip-types --test connection.test.ts
node node_modules/expo/bin/cli export --platform android
```

The app loads the UI from the selected server, so ACP and terminal capabilities
follow that server's Forge version. It does not run a Forge daemon on the phone
or provide offline workspaces.
