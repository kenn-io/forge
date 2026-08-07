# iOS-Compatible Documentation SVG Design

## Problem

The generated workflow screenshots are SVG files whose only visual content is
an XHTML document inside `<foreignObject>`. Chromium scales that content when
the SVG is used through `<img>`, but WebKit paints the XHTML at its original
1280-pixel width and clips it into the responsive image box. The result is a
magnified left edge on iOS and Safari.

## Decision

Keep the generated assets as SVG, but export the browser's completed paint as
native SVG geometry. Chromium will print each stabilized 1280 by 820 page to a
single-page vector PDF. Poppler's `pdftocairo` will convert that page to SVG
paths, masks, clips, gradients, and embedded icon images. The generator will
then normalize the root dimensions to 1280 by 820 and add the existing title
and description metadata.

Poppler is a build-only dependency. Install `poppler-utils` beside the existing
Playwright runtime packages in Vercel and the repository Playwright image.
Local documentation contributors install Poppler through their platform
package manager. The exporter must remain compatible with Amazon Linux 2's
Poppler 0.26.5 (`poppler-utils-0.26.5-43.amzn2.1.7`); that version produces a
1280 by 820 native SVG that scales correctly in WebKit.

## Constraints

- Preserve the current `.svg` filenames, light/dark selection, captions,
  lightbox behavior, seeded data, and 1280 by 820 intrinsic dimensions.
- Generated screenshot files remain build artifacts and must not be tracked.
- Generated SVGs must contain no `<foreignObject>`, scripts, remote asset URLs,
  private filesystem paths, or active animation.
- Do not add a raster fallback or browser-specific serving branch.
- A missing or failed converter must stop the docs build with a clear error.

## Verification

The screenshot suite first gains an assertion that rejects `<foreignObject>`;
it must fail against the existing serializer. After implementation, generate
all twelve assets, build the rendered site, and run the complete rendered-site
suite. Inspect the light and dark overview assets. Finally, load the rendered
site with an iPhone-sized WebKit context and verify the full 1280 by 820 asset
fits its responsive image bounds without the magnified-left-edge failure.
