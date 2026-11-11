/**
 * Stream utilities for byte-level manipulation.
 */

/**
 * Reads `body` to completion and discards it.
 *
 * Cancelling a request body the client is still streaming resets the request
 * stream. Firefox surfaces that to the caller as NS_ERROR_NET_PARTIAL_TRANSFER
 * even when the response is a success, so any path that returns 2xx while the
 * upload is still in flight must drain instead of cancel. Rejection paths
 * should keep cancelling — accepting bytes only to throw them away is worse.
 *
 * Draining costs the inbound bandwidth. Clients that want to avoid the transfer
 * altogether should preflight with HEAD /upload (BUD-06).
 *
 * Never throws: a client vanishing mid-drain is not worth failing a response
 * that has already been decided.
 */
export async function drainBody(
  body: ReadableStream<Uint8Array> | null,
): Promise<void> {
  if (!body) return;
  await body.pipeTo(new WritableStream()).catch(() => {});
}

/**
 * Returns a TransformStream that passes bytes through until exactly `limit`
 * bytes have been forwarded, then closes the readable side.
 *
 * Used by storage adapters to implement native range reads:
 *   file.readable.pipeThrough(byteLimitTransform(end - start + 1))
 *
 * @param limit Maximum number of bytes to forward. Must be >= 0.
 */
export function byteLimitTransform(
  limit: number,
): TransformStream<Uint8Array, Uint8Array> {
  let remaining = limit;

  return new TransformStream<Uint8Array, Uint8Array>({
    transform(chunk, controller) {
      if (remaining <= 0) {
        controller.terminate();
        return;
      }

      if (chunk.byteLength <= remaining) {
        controller.enqueue(chunk);
        remaining -= chunk.byteLength;
      } else {
        // Partial chunk — enqueue only what we still need
        controller.enqueue(chunk.subarray(0, remaining));
        remaining = 0;
      }

      if (remaining <= 0) {
        controller.terminate();
      }
    },
  });
}
