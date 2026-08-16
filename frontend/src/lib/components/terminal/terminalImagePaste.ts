export type PastedImageUploadResult =
  | { readonly _tag: "Success"; readonly path: string }
  | { readonly _tag: "Failure" };

function imageFile(file: File | null): file is File {
  return file !== null && file.type.startsWith("image/");
}

export function clipboardImageFiles(data: DataTransfer | null): readonly File[] {
  if (data === null) return [];
  const itemImages = Array.from(data.items ?? [])
    .filter((item) => item.kind === "file" && item.type.startsWith("image/"))
    .map((item) => item.getAsFile())
    .filter(imageFile);
  if (itemImages.length > 0) return itemImages;
  return Array.from(data.files ?? []).filter(imageFile);
}

export function formatPastedImagePaths(results: readonly PastedImageUploadResult[]): {
  readonly text: string;
  readonly failed: number;
} {
  const paths = results.flatMap((result) => (result._tag === "Success" ? [result.path] : []));
  return {
    text: paths.join(" "),
    failed: results.length - paths.length,
  };
}
