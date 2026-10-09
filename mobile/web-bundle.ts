import { Asset } from "expo-asset";
import { File } from "expo-file-system";

let bundle: string | undefined;

export async function loadWebBundle(): Promise<string> {
  if (bundle === undefined) {
    const asset = await Asset.fromModule(require("./assets/web.bundle")).downloadAsync();
    bundle = await new File(asset.localUri!).text();
  }
  return bundle;
}
