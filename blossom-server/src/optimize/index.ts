/**
 * optimizeMedia() — MIME detection + dispatch to image or video optimizer.
 *
 * MIME detection order:
 *   1. Extension-based via @std/media-types getType()
 *   2. Magic-byte fallback via npm:file-type fileTypeFromBuffer()
 *   3. Unknown → throw Error("Unsupported file type")
 *
 * All media processing modules (image.ts, video.ts) and their native
 * dependencies (sharp, file-type) are loaded lazily via
 * dynamic import so they are never required when the media endpoint is
 * disabled.
 */

import { typeByExtension } from "@std/media-types";
import { extname } from "@std/path";
import type { MediaConfig } from "../config/schema.ts";

/**
 * Detects MIME type of a file on disk.
 * Tries extension first, then reads the first 4096 bytes for magic-byte detection.
 */
async function detectMimeType(filePath: string): Promise<string | null> {
  // 1. Extension-based: strip the leading dot from the extension
  const ext = extname(filePath).replace(/^\./, "");
  if (ext) {
    const extMime = typeByExtension(ext);
    if (extMime) return extMime;
  }

  // 2. Magic-byte fallback — load file-type lazily
  const { fileTypeFromBuffer } = await import("file-type");
  const file = await Deno.open(filePath, { read: true });
  try {
    const buf = new Uint8Array(4096);
    const n = await file.read(buf);
    if (n === null) return null;
    const result = await fileTypeFromBuffer(buf.subarray(0, n));
    return result?.mime ?? null;
  } finally {
    file.close();
  }
}

export async function optimizeMedia(
  inputPath: string,
  config: MediaConfig,
): Promise<string> {
  await Deno.mkdir(config.tmpDir, { recursive: true });

  const mime = await detectMimeType(inputPath);

  if (!mime) {
    throw new Error("Unsupported file type: could not determine MIME type");
  }

  let outputPath: string | null = null;

  try {
    if (mime === "image/gif") {
      const { optimizeGif } = await import("./image.ts");
      outputPath = await optimizeGif(inputPath, config.image, config.tmpDir);
    } else if (
      mime === "image/jpeg" || mime === "image/png" || mime === "image/webp"
    ) {
      const { optimizeImage } = await import("./image.ts");
      outputPath = await optimizeImage(inputPath, config.image, config.tmpDir);
    } else if (mime.startsWith("video/")) {
      const { optimizeVideo } = await import("./video.ts");
      outputPath = await optimizeVideo(inputPath, config.video, config.tmpDir);
    } else {
      throw new Error(`Unsupported file type: ${mime}`);
    }
  } catch (err) {
    if (outputPath) {
      await Deno.remove(outputPath).catch(() => {});
    }
    const msg = err instanceof Error ? err.message : String(err);
    throw new Error(`Optimization failed: ${msg}`);
  }

  return outputPath;
}
